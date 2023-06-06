package main

import (
	"encoding/json"
	"flag"
	"log"
	"math/rand"
	"os"
	"runtime"
	"strconv"
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
	configPath := flag.String("config", "config/config.json", "Path to config file")
	primePort := flag.Int("prime", -1, "Prime upstream port (overrides config)")
	regionPort := flag.Int("region", -1, "Region upstream port (overrides config)")
	zonePort := flag.Int("zone", -1, "Zone upstream port (overrides config)")

	stratumPort := flag.Int("stratum", -1, "Stratum listen port (overrides config)")

	flag.Parse()

	log.Printf("Loading config: %v", configPath)

	// Read config file.
	configFile, err := os.Open(*configPath)
	if err != nil {
		log.Fatal("File error: ", err.Error())
	}
	defer configFile.Close()
	jsonParser := json.NewDecoder(configFile)
	if err := jsonParser.Decode(&cfg); err != nil {
		log.Fatal("Config error: ", err.Error())
	}

	// Perform custom overrides.
	if primePort != nil && *primePort != -1 {
		cfg.Upstream[common.PRIME_CTX].Url = "http://localhost:" + strconv.Itoa(*primePort)
	}
	if regionPort != nil && *regionPort != -1 {
		cfg.Upstream[common.REGION_CTX].Url = "http://localhost:" + strconv.Itoa(*regionPort)
	}
	if zonePort != nil && *zonePort != -1 {
		cfg.Upstream[common.ZONE_CTX].Url = "http://localhost:" + strconv.Itoa(*zonePort)
	}
	if *stratumPort != -1 {
		cfg.Proxy.Stratum.Listen = "localhost:" + strconv.Itoa(*stratumPort)
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
