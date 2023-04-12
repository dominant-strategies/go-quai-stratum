package main

import (
	"encoding/json"
	"log"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/J-A-M-P-S/structs"

	"github.com/dominant-strategies/go-quai-stratum/api"
	"github.com/dominant-strategies/go-quai-stratum/proxy"
	"github.com/dominant-strategies/go-quai-stratum/storage"
	"github.com/dominant-strategies/go-quai/common"
)

var cfg proxy.Config
var backend *storage.RedisClient

func startProxy() {
	s := proxy.NewProxy(&cfg, backend)
	s.Start()
}

func startApi() {
	settings := structs.Map(&cfg)
	s := api.NewApiServer(&cfg.Api, settings, backend)
	s.Start()
}

func readConfig(cfg *proxy.Config) {
	configFileName := "config/config.json"
	configFileName, _ = filepath.Abs(configFileName)
	log.Printf("Loading config: %v", configFileName)

	configFile, err := os.Open(configFileName)
	if err != nil {
		log.Fatal("File error: ", err.Error())
	}
	defer configFile.Close()
	jsonParser := json.NewDecoder(configFile)
	if err := jsonParser.Decode(&cfg); err != nil {
		log.Fatal("Config error: ", err.Error())
	}
	if len(os.Args) == 5 {
		cfg.Upstream[common.PRIME_CTX].Url = os.Args[1]
		cfg.Upstream[common.REGION_CTX].Url = os.Args[2]
		cfg.Upstream[common.ZONE_CTX].Url = os.Args[3]
		cfg.Proxy.Stratum.Listen = os.Args[4]
	}
}

func init() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
}

func main() {
	readConfig(&cfg)
	rand.Seed(time.Now().UnixNano())

	if cfg.Threads > 0 {
		runtime.GOMAXPROCS(cfg.Threads)
		log.Printf("Running with %v threads", cfg.Threads)
	}

	backend = storage.NewRedisClient(&cfg.Redis, cfg.Coin)
	pong, err := backend.Check()
	if err != nil {
		log.Printf("Can't establish connection to backend: %v", err)
	} else {
		log.Printf("Backend check reply: %v", pong)
	}

	if cfg.Proxy.Enabled {
		go startProxy()
	}
	if cfg.Api.Enabled {
		go startApi()
	}

	quit := make(chan bool)
	<-quit
}
