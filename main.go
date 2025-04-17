package main

import (
	"embed"
	"encoding/json"
	"flag"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"time"

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

// MinerManager to track the GPU miner process
type MinerManager struct {
	cmd     *exec.Cmd
	running bool
}

// Global instance of MinerManager
var minerManager MinerManager

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

	gpuType := flag.String("gpuType", "", "Gpu type either (nvidia/amd)")

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

	if gpuType != nil && *gpuType != "" {
		cfg.Mining.GpuType = *gpuType
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

//go:embed gpu-miner/quai-gpu-miner-*
var gpuMiners embed.FS

func startGpuMiner(config proxy.Config) {

	// Delay for the proxy to fully initialize.
	time.Sleep(5 * time.Second)
	// Define the binary name based on GPU type
	binaryName := "quai-gpu-miner-" + config.Mining.GpuType
	embeddedPath := "gpu-miner/" + binaryName

	// Read the embedded binary
	binaryData, err := gpuMiners.ReadFile(embeddedPath)
	if err != nil {
		log.Global.Errorf("Failed to read embedded binary %s: %v", binaryName, err)
		return
	}

	// Create a temporary file or use a fixed path for the binary
	binaryPath := filepath.Join("gpu-miner", binaryName)
	if err := os.MkdirAll("gpu-miner", 0755); err != nil {
		log.Global.Errorf("Failed to create gpu-miner directory: %v", err)
		return
	}

	// Write the binary to disk
	if err := os.WriteFile(binaryPath, binaryData, 0755); err != nil {
		log.Global.Errorf("Failed to write binary %s: %v", binaryName, err)
		return
	}

	// Ensure the binary is executable (important for Linux/macOS)
	if err := os.Chmod(binaryPath, 0755); err != nil {
		log.Global.Errorf("Failed to set executable permissions for %s: %v", binaryName, err)
		return
	}

	// Initialize the command
	minerManager.cmd = exec.Command(binaryPath, "-U", "-P", "stratum://"+config.Proxy.Stratum.Listen)
	if err := minerManager.cmd.Start(); err != nil {
		log.Global.Warnf("Failed to start GPU miner: %v", err)
		return
	}
	minerManager.running = true
	log.Global.Info("GPU miner running...")
}

// StopAllGpuMiners kills all quai-gpu-miner processes
func StopAllGpuMiners() {
	var cmd *exec.Cmd
	// Use platform-specific command to kill all quai-gpu-miner processes
	switch runtime.GOOS {
	case "linux", "darwin":
		// Use pkill to kill all processes matching the name
		cmd = exec.Command("pkill", "-f", "quai-gpu-miner")
	}

	// Execute the kill command
	if err := cmd.Run(); err != nil {
		log.Global.Warnf("Failed to kill all GPU miners: %v", err)
	} else {
		log.Global.Info("All GPU miners stopped")
	}

	// Reset the minerManager state
	minerManager.running = false
	minerManager.cmd = nil
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

	if cfg.Mining.Enabled {
		go startGpuMiner(cfg)
	}

	// Set up signal handling for graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)

	// Block until a signal is received
	<-quit
	log.Global.Info("Received shutdown signal, stopping...")

	if cfg.Mining.Enabled {
		// kill the gpu miner on stop
		StopAllGpuMiners()
	}

	log.Global.Info("Stratum stopped")
}
