package main

import (
	"encoding/json"
	"log"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"time"
	// "lukechampine.com/blake3"
	// "github.com/dominant-strategies/go-quai/common"

	// "encoding/hex"

	"github.com/J-A-M-P-S/structs"

	"github.com/dominant-strategies/go-quai-stratum/api"
	"github.com/dominant-strategies/go-quai-stratum/proxy"
	"github.com/dominant-strategies/go-quai-stratum/storage"
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
	if len(os.Args) > 1 {
		configFileName = os.Args[1]
	}
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

	// log.Print("Hash procedure following");
	// sealHash := []byte{0xb3, 0xe6, 0x35, 0xe4, 0x9d, 0x7e, 0xa6, 0x86, 0xe7, 0xaa, 0xe6, 0xae, 0x90, 0xa6, 0x51, 0xa0, 0x01, 0x8e, 0x48, 0x21, 0xdb, 0x42, 0x41, 0x9b, 0xfc, 0xb7, 0xd0, 0xe5, 0xb7, 0x11, 0xa5, 0xa0}
	// sealHashStr := "00070000161FE401202E1345D2D2F38D57DD5715F7F54953CDE71B2D7CCD45EC411500001B25550E7BE2D69B5F764BA833A02943A656605B320E79A6DC4DDA88924A000009756A21E45136379B925BF024D9E6D4134A92BEDE7BE5BC32801C2AE08F00001E4218BFC7F7126F97192C3AE193EC5526EAB4F0EDB090246216F1AA80C000002349B6AF9967DE46FC5E7CCE9858FB8A7B7EF334E194AB2A040B21E511B100000225E862DDFED99CDA3F859A01758A58C90876C703676E0DCCB35951F97200001832F64F200D39DA63F58D059D062BA6CE7DF1218A0AF6A1AD6CB859A1334A454D47AFC7672488D906F7699D6A2F73FAE33C265AFC6D9EDA3B60351CD2FA60CD823EBDD1605CD2FF4A80C3540DD93AD20A9957519D4DC9612052DC4ED11B0000017BF884C7A71E245671"
	// sealHash, err := hex.DecodeString(sealHashStr)
	// if err != nil {
	// 	log.Fatalf("Unable to decode sealHash: %v", err);
	// }
	// sealHash_flipped := flipEndianness(sealHash)

	// nonceStr := "2197411ef176a654266c6aa627593edaa81dba15a37c9974"
	// nonce, err := hex.DecodeString(nonceStr)
	// if err != nil {
	// 	log.Fatalf("Unable to decode nonce: %v", err);
	// }

	// nonce_flipped := flipEndianness(nonce)
	// bytes_written := copy(hData[:], nonce_flipped[:])
	// if bytes_written != 24 {
	// 	log.Fatalf("Unable to copy nonce");
	// }

	// var hData []byte
	// bytes_written := copy(hData[8:], sealHash)
	// log.Print(bytes_written)
	// if bytes_written != 32 {
	// 	log.Fatalf("Unable to copy sealHash");
	// }

	// hData_flipped := flipEndianness(hData[:])

	// hash_arr := append(nonce, sealHash...)
	// hash_arr := append(nonce, sealHash...)

	// sum := blake3.Sum256(hash_arr[:])
	// newsom := blake3.Sum256(sum[:])
	// var hash common.Hash;
	// hash.SetBytes(sum[:])

	// log.Print("Hash: " + hex.EncodeToString(newsom[:]))
	// log.Print(hash);

	quit := make(chan bool)
	<-quit
}
