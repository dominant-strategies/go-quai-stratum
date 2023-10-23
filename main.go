package main

import (
	"encoding/json"
	"flag"
	"log"
	"os"
	"runtime"
	"strconv"

	"github.com/J-A-M-P-S/structs"

	"github.com/dominant-strategies/go-quai-stratum/api"
	"github.com/dominant-strategies/go-quai-stratum/proxy"
	"github.com/dominant-strategies/go-quai-stratum/storage"
	"github.com/dominant-strategies/go-quai/common"
)

var cfg proxy.Config
var backend *storage.RedisClient

var portDefinitions = map[string]string{
	"prime":   "8547",
	"cyprus":  "8579",
	"paxos":   "8581",
	"hydra":   "8583",
	"cyprus1": "8611",
	"cyprus2": "8643",
	"cyprus3": "8675",
	"paxos1":  "8613",
	"paxos2":  "8645",
	"paxos3":  "8677",
	"hydra1":  "8615",
	"hydra2":  "8647",
	"hydra3":  "8679",
}

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
	primePort := flag.String("prime", "prime", "Prime upstream port (overrides config)")
	regionPort := flag.String("region", "", "Region upstream port (overrides config)")
	zonePort := flag.String("zone", "", "Zone upstream port (overrides config)")

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
	if primePort != nil && *primePort != "prime" {
		cfg.Upstream[common.PRIME_CTX].Url = "ws://127.0.0.1:" + returnPortHelper(*primePort)
	}
	if regionPort != nil && *regionPort != "" {
		cfg.Upstream[common.REGION_CTX].Url = "ws://127.0.0.1:" + returnPortHelper(*regionPort)
	}
	if zonePort != nil && *zonePort != "" {
		cfg.Upstream[common.ZONE_CTX].Url = "ws://127.0.0.1:" + returnPortHelper(*zonePort)
	}
	if *stratumPort != -1 {
		cfg.Proxy.Stratum.Listen = "0.0.0.0:" + strconv.Itoa(*stratumPort)
	}
}

func returnPortHelper(portStr string) string {
	// Check if already a port number, otherwise look up by name.
	if _, err := strconv.Atoi(portStr); err != nil {
		portStr = portDefinitions[portStr]
	}
	return portStr
}

func init() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.SetOutput(os.Stdout)
}

func main() {
	readConfig(&cfg)

	if cfg.Threads > 0 {
		runtime.GOMAXPROCS(cfg.Threads)
		log.Printf("Running with %v threads", cfg.Threads)
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
