package redisbayes

import (
	"fmt"
	"github.com/garyburd/redigo/redis"
	"github.com/kylelemons/go-gypsy/yaml"
	"log"
	"math"
	"regexp"
	"strings"
)

var (
	english_ignore_words_map = make(map[string]int)
	redis_conn               redis.Conn
	redis_prefix             = "bayes:"
	correction               = 0.1
)

// replace \_.,<>:;~+|\[\]?`"!@#$%^&*()\s chars with whitespace
// re.sub(r'[\_.,<>:;~+|\[\]?`"!@#$%^&*()\s]', ' ' 
func Tidy(s string) string {
	reg, err := regexp.Compile("[\\_.,:;~+|\\[\\]?`\"!@#$%^&*()\\s]+")
	if err != nil {
		log.Fatal(err)
	}

	text_in_lower := strings.ToLower(s)
	safe := reg.ReplaceAllLiteralString(text_in_lower, " ")

	return safe
}

// tidy the input text, ignore those text composed with less than 2 chars 
func English_tokenizer(s string) []string {
	words := strings.Fields(Tidy(s))

	for index, word := range words {
		strings.TrimSpace(word)
		_, omit := english_ignore_words_map[word]
		if omit || len(word) < 2 {
			words = words[:index+copy(words[index:], words[index+1:])]
		}
	}

	return words
}

// compute word occurances
func Occurances(words []string) map[string]uint {
	counts := make(map[string]uint)
	for _, word := range words {
		if _, ok := counts[word]; ok {
			counts[word] += 1
		} else {
			counts[word] = 1
		}
	}

	return counts
}

func Flush() {
	reply, err := redis_conn.Do("SMEMBERS", redis_prefix+"categories")
	if err != nil {
		log.Println(err)
		return
	}

	for _, key := range reply.([]string) {
		redis_conn.Do("DELETE", key)
	}

	redis_conn.Do("DELETE", redis_prefix+"categories")
}

func Train(categories, text string) {
	redis_conn.Do("SADD", redis_prefix+"categories", categories)

	token_occur := Occurances(English_tokenizer(text))
	for word, count := range token_occur {
		redis_conn.Do("HINCRBY", redis_prefix+categories, word, count)
	}
}

func Untrain(categories, text string) {
	token_occur := Occurances(English_tokenizer(text))
	for word, count := range token_occur {
		cur, err := redis_conn.Do("HGET", redis_prefix+categories, word)
		if err != nil {
			log.Println(err)
			return
		}

		if cur := cur.(uint); cur != 0 {
			inew := cur - count
			if inew > 0 {
				redis_conn.Do("HSET", redis_prefix+categories, word, inew)
			} else {
				redis_conn.Do("HDEL", redis_prefix+categories, word)
			}
		}
	}

	if Tally(categories) == 0 {
		redis_conn.Do("DELETE", redis_prefix+categories)
		redis_conn.Do("SREM", redis_prefix+"categories", categories)
	}
}

func Classify(text string) string {
	scores := Score(text)
	max := 0.0
	key := ""
	if scores != nil {
		for k, v := range scores {
			if v >= max {
				max = v
				key = k
			}
		}

		return key
	}

	return ""
}

func Score(text string) map[string]float64 {
	token_occur := Occurances(English_tokenizer(text))
	res := make(map[string]float64)

	reply, err := redis_conn.Do("SMEMBERS", redis_prefix+"categories")
	if err != nil {
		log.Println(err)
		return nil
	}

	for _, category := range reply.([]string) {
		tally := Tally(category)
		if tally == 0 {
			continue
		}

		res[category] = 0.0
		for word, _ := range token_occur {
			score, err := redis_conn.Do("HGET", redis_prefix+category, word)
			if err != nil {
				log.Println(err)
				return nil
			}

			if score.(float64) == 0.0 {
				score = correction
			}
			res[category] += math.Log(score.(float64) / float64(tally))
		}
	}
	return res
}

func Tally(category string) (sum uint) {
	vals, err := redis_conn.Do("HVALS", redis_prefix+category)
	if err != nil {
		log.Println(err)
		return
	}

	for _, val := range vals.([]uint) {
		sum += val
	}
	return sum
}

// init function, load the configs
// fill english_ignore_words_map
func init() {
	// load config file
	cfg_filename := "config.yaml"
	config, err := yaml.ReadFile(cfg_filename)
	if err != nil {
		log.Fatalf("readfile(%s): %s", cfg_filename, err)
	}

	// get english ignore entire string
	english_ignore, err := config.Get("english_ignore")
	if err != nil {
		log.Fatalf("%s parse error: %s\n", english_ignore, err)
	}

	// get each separated words
	english_ignore_words_list := strings.Fields(english_ignore)
	for _, word := range english_ignore_words_list {
		word = strings.TrimSpace(word)
		english_ignore_words_map[word] = 1
	}

	// get redis connection info
	redis_config, err := yaml.Child(config.Root, "redis_server")
	if err != nil {
		log.Fatalf("redis config parse error: %s\n", err)
	}

	redis_config_m := redis_config.(yaml.Map)
	host, port := redis_config_m["host"], redis_config_m["port"]
	redis_conn, err = redis.Dial("tcp", fmt.Sprintf("%s:%s", host, port))
	defer redis_conn.Close()
	if err != nil {
		log.Fatalf("Can not connect to Redis Server: %s", err)
	}
}
