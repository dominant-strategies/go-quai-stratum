package main

import (
	"encoding/json"
	"flag"
	"os"
	"runtime"
	"strconv"

	"github.com/J-A-M-P-S/structs"

	"github.com/dominant-strategies/go-quai-stratum/api"
	"github.com/dominant-strategies/go-quai-stratum/proxy"
	"github.com/dominant-strategies/go-quai-stratum/storage"
	"github.com/dominant-strategies/go-quai-stratum/util"

	"github.com/dominant-strategies/go-quai/cmd/utils"
	"github.com/dominant-strategies/go-quai/common"
	"github.com/dominant-strategies/go-quai/log"
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
	primePort := flag.String("prime", "", "Prime upstream port (overrides config)")
	regionPort := flag.String("region", "", "Region upstream port (overrides config)")
	zonePort := flag.String("zone", "", "Zone upstream port (overrides config)")

	stratumPort := flag.Int("stratum", -1, "Stratum listen port (overrides config)")

	flag.Parse()

	log.Global.WithField(
		"path", *configPath,
	).Info("Loading config")

	// Read config file.
	configFile, err := os.Open(*configPath)
	if err != nil {
		log.Global.Fatal("File error: ", err.Error())
	}
	defer configFile.Close()
	jsonParser := json.NewDecoder(configFile)
	if err := jsonParser.Decode(&cfg); err != nil {
		log.Global.Fatal("Config error: ", err.Error())
	}

	// Perform custom overrides. Default means they weren't set on the command line.
	if primePort != nil && *primePort != "" {
		cfg.Upstream[common.PRIME_CTX].Name = *primePort
		cfg.Upstream[common.PRIME_CTX].Url = "ws://127.0.0.1:" + returnPortHelper(*primePort)
	}
	if regionPort != nil && *regionPort != "" {
		cfg.Upstream[common.REGION_CTX].Name = *regionPort
		cfg.Upstream[common.REGION_CTX].Url = "ws://127.0.0.1:" + returnPortHelper(*regionPort)
	}
	if zonePort != nil && *zonePort != "" {
		cfg.Upstream[common.ZONE_CTX].Name = *zonePort
		cfg.Upstream[common.ZONE_CTX].Url = "ws://127.0.0.1:" + returnPortHelper(*zonePort)
	}
	if *stratumPort != -1 {
		cfg.Proxy.Stratum.Listen = "0.0.0.0:" + strconv.Itoa(*stratumPort)
	}
}

func returnPortHelper(locName string) string {
	var portStr string
	// Check if already a port number, otherwise look up by name.
	if _, err := strconv.Atoi(locName); err != nil {
		loc, err := util.LocationFromName(locName)
		if err != nil {
			log.Global.WithField("err", err).Warn("Unable to parse location")
		}
		portStr = strconv.Itoa(utils.GetWSPort(loc))
	}
	return portStr
}

func init() {
}

func main() {
	readConfig(&cfg)

	if cfg.Threads > 0 {
		runtime.GOMAXPROCS(cfg.Threads)
		log.Global.WithField(
			"threads", cfg.Threads,
		).Debug("Threads running")
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
