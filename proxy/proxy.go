package proxy

import (
	"encoding/json"
	"log"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dominant-strategies/go-quai/common"
	"github.com/gorilla/mux"

	"github.com/dominant-strategies/go-quai-stratum/policy"
	"github.com/dominant-strategies/go-quai-stratum/rpc"
	"github.com/dominant-strategies/go-quai-stratum/storage"
	"github.com/dominant-strategies/go-quai-stratum/util"
)

type ProxyServer struct {
	config             *Config
	blockTemplate      atomic.Value
	upstreams          []*rpc.RPCClient
	backend            *storage.RedisClient
	diff               string
	policy             *policy.PolicyServer
	hashrateExpiration time.Duration
	failsCount         int64

	// Stratum
	sessionsMu sync.RWMutex
	sessions   map[*Session]struct{}
	timeout    time.Duration
	Extranonce string
}

type jobDetails struct {
	JobID      string
	SeedHash   string
	HeaderHash string
}

type Session struct {
	ip  string
	enc *json.Encoder

	// Stratum
	sync.Mutex
	conn           *net.TCPConn
	login          string
	subscriptionID string
	Extranonce     string
	JobDetails     jobDetails
}

func NewProxy(cfg *Config, backend *storage.RedisClient) *ProxyServer {
	if len(cfg.Name) == 0 {
		log.Fatal("You must set instance name")
	}
	policy := policy.Start(&cfg.Proxy.Policy, backend)

	proxy := &ProxyServer{config: cfg, upstreams: make([]*rpc.RPCClient, common.HierarchyDepth), backend: backend, policy: policy}
	proxy.diff = util.GetTargetHex(cfg.Proxy.Difficulty)

	for level := 0; level < common.HierarchyDepth; level++ {
		// Eventually we should verify with the "name" that it's in the correct order.
		proxy.upstreams[level] = rpc.NewRPCClient(
			cfg.Upstream[level].Name,
			cfg.Upstream[level].Url,
			cfg.Upstream[level].Timeout,
		)
	}

	if cfg.Proxy.Stratum.Enabled {
		proxy.sessions = make(map[*Session]struct{})
		go proxy.ListenTCP()
	}

	proxy.fetchBlockTemplate()

	proxy.hashrateExpiration = util.MustParseDuration(cfg.Proxy.HashrateExpiration)

	refreshIntv := util.MustParseDuration(cfg.Proxy.BlockRefreshInterval)
	refreshTimer := time.NewTimer(refreshIntv)
	log.Printf("Set block refresh every %v", refreshIntv)

	stateUpdateIntv := util.MustParseDuration(cfg.Proxy.StateUpdateInterval)
	stateUpdateTimer := time.NewTimer(stateUpdateIntv)

	go func() {
		for {
			select {
			case <-refreshTimer.C:
				proxy.fetchBlockTemplate()
				refreshTimer.Reset(refreshIntv)
			}
		}
	}()

	go func() {
		for {
			select {
			case <-stateUpdateTimer.C:
				t := proxy.currentBlockTemplate()
				if t != nil {
					rpc := proxy.rpc(common.ZONE_CTX)
					height := t.Height[common.ZONE_CTX].Int64() - 1
					prev := height - cfg.BlockTimeWindow
					if prev < 0 {
						prev = 0
					}
					n := height - prev
					if n > 0 {
						block, err := rpc.GetBlockByHeight(height)
						if err != nil || block == nil {
							log.Printf("Error while retrieving block from node: %v", err)
							proxy.markSick()
						} else {
							timestamp := block.Time()
							prevblock, _ := rpc.GetBlockByHeight(prev)
							prevTime := prevblock.Time()
							blocktime := float64(timestamp-prevTime) / float64(n)
							err = backend.WriteNodeState(cfg.Name, t.Height[common.ZONE_CTX].Uint64()-1, t.Difficulty, blocktime)
							if err != nil {
								log.Printf("Failed to write node state to backend: %v", err)
								proxy.markSick()
							} else {
								proxy.markOk()
							}
						}
					} else {
						err := backend.WriteNodeState(cfg.Name, t.Height[common.ZONE_CTX].Uint64()-1, t.Difficulty, cfg.AvgBlockTime)
						if err != nil {
							log.Printf("Failed to write node state to backend: %v", err)
							proxy.markSick()
						} else {
							proxy.markOk()
						}
					}
				}
				stateUpdateTimer.Reset(stateUpdateIntv)
			}
		}
	}()

	return proxy
}

func (s *ProxyServer) Start() {
	log.Printf("Starting proxy on %v", s.config.Proxy.Listen)
	r := mux.NewRouter()
	srv := &http.Server{
		Addr:           s.config.Proxy.Listen,
		Handler:        r,
		MaxHeaderBytes: s.config.Proxy.LimitHeadersSize,
	}
	err := srv.ListenAndServe()
	if err != nil {
		log.Fatalf("Failed to start proxy: %v", err)
	}
}

func (s *ProxyServer) rpc(level int) *rpc.RPCClient {
	return s.upstreams[level]
}

func (s *ProxyServer) currentBlockTemplate() *BlockTemplate {
	t := s.blockTemplate.Load()
	if t != nil {
		return t.(*BlockTemplate)
	} else {
		return nil
	}
}

func (s *ProxyServer) markSick() {
	atomic.AddInt64(&s.failsCount, 1)
}

func (s *ProxyServer) isSick() bool {
	x := atomic.LoadInt64(&s.failsCount)
	if s.config.Proxy.HealthCheck && x >= s.config.Proxy.MaxFails {
		return true
	}
	return false
}

func (s *ProxyServer) markOk() {
	atomic.StoreInt64(&s.failsCount, 0)
}
