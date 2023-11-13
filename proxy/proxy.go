package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"math/rand"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dominant-strategies/go-quai-stratum/policy"
	"github.com/dominant-strategies/go-quai-stratum/storage"
	"github.com/dominant-strategies/go-quai-stratum/util"
	"github.com/dominant-strategies/go-quai/common"
	"github.com/dominant-strategies/go-quai/consensus"
	"github.com/dominant-strategies/go-quai/consensus/progpow"
	"github.com/dominant-strategies/go-quai/core/types"
	"github.com/dominant-strategies/go-quai/quaiclient/ethclient"

	"github.com/gorilla/mux"
	lru "github.com/hashicorp/golang-lru/v2/expirable"
)

const (
	c_updateChSize = 20
)

type ProxyServer struct {
	config             *Config
	blockTemplate      atomic.Value
	upstreams          *[]Upstream
	clients            SliceClients
	backend            *storage.RedisClient
	diff               string
	policy             *policy.PolicyServer
	hashrateExpiration time.Duration
	failsCount         int64
	engine             consensus.Engine
	rng                *rand.Rand

	// Channel to receive header updates
	updateCh chan *types.Header

	// Keep track of previous headers
	headerCache *lru.LRU[uint, *types.Header]

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

type SliceClients [common.HierarchyDepth]*ethclient.Client

func NewProxy(cfg *Config, backend *storage.RedisClient) *ProxyServer {
	if len(cfg.Name) == 0 {
		log.Fatal("You must set instance name")
	}
	policy := policy.Start(&cfg.Proxy.Policy, backend)

	proxy := &ProxyServer{
		config:    cfg,
		upstreams: &cfg.Upstream,
		backend:   backend,
		policy:    policy,
		engine: progpow.New(
			progpow.Config{},
			nil,
			false,
		),
		updateCh:    make(chan *types.Header, c_updateChSize),
		headerCache: lru.NewLRU[uint, *types.Header](10, nil, 600*time.Second),
	}
	proxy.diff = util.GetTargetHex(cfg.Proxy.Difficulty)

	proxy.clients = proxy.connectToSlice()

	proxy.rng = rand.New(rand.NewSource(time.Now().UnixNano()))

	if cfg.Proxy.Stratum.Enabled {
		proxy.sessions = make(map[*Session]struct{})
		go proxy.ListenTCP()
	}

	proxy.hashrateExpiration = util.MustParseDuration(cfg.Proxy.HashrateExpiration)

	refreshIntv := util.MustParseDuration(cfg.Proxy.BlockRefreshInterval)
	refreshTimer := time.NewTimer(refreshIntv)
	log.Printf("Set block refresh every %v", refreshIntv)

	stateUpdateIntv := util.MustParseDuration(cfg.Proxy.StateUpdateInterval)
	stateUpdateTimer := time.NewTimer(stateUpdateIntv)
	proxy.fetchBlockTemplate()

	go func() {
		for {
			select {
			case <-refreshTimer.C:
				proxy.fetchBlockTemplate()
				refreshTimer.Reset(refreshIntv)
			case newPendingHeader := <-proxy.updateCh:
				proxy.updateBlockTemplate(newPendingHeader)
			}
		}
	}()

	if cfg.Redis.Enabled {
		go func() {
			for {
				select {
				case <-stateUpdateTimer.C:
					t := proxy.currentBlockTemplate()
					if t != nil {
						height := t.Height[common.ZONE_CTX].Int64() - 1
						prev := height - cfg.BlockTimeWindow
						if prev < 0 {
							prev = 0
						}
						n := height - prev
						if n > 0 {
							block, err := proxy.clients[common.ZONE_CTX].BlockByNumber(context.Background(), big.NewInt(height))
							if err != nil || block == nil {
								log.Printf("Error while retrieving block from node: %v", err)
								proxy.markSick()
							} else {
								timestamp := block.Time()
								prevblock, _ := proxy.clients[common.ZONE_CTX].BlockByNumber(context.Background(), big.NewInt(prev))
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
	}

	return proxy
}

// connectToSlice takes in a config and retrieves the Prime, Region, and Zone client
// that is used for mining in a slice.
func (s *ProxyServer) connectToSlice() SliceClients {
	var err error
	clients := SliceClients{}
	primeConnected := false
	regionConnected := false
	zoneConnected := false
	primeErrorPrinted := false
	regionErrorPrinted := false
	zoneErrorPrinted := false

	primeUrl := (*s.upstreams)[common.PRIME_CTX].Url
	regionUrl := (*s.upstreams)[common.REGION_CTX].Url
	if regionUrl == "" {
		log.Fatal("Please specify region port!")
	}
	zoneUrl := (*s.upstreams)[common.ZONE_CTX].Url
	if zoneUrl == "" {
		log.Fatal("Please specify zone port!")
	}

	for !primeConnected || !regionConnected || !zoneConnected {
		if primeUrl != "" && !primeConnected {
			clients[common.PRIME_CTX], err = ethclient.Dial(primeUrl)
			if err != nil {
				if !primeErrorPrinted {
					log.Println("Unable to connect to node:", "Prime", primeUrl)
					primeErrorPrinted = true
				}
			} else {
				primeConnected = true
				log.Println("Connected to Prime at: ", primeUrl)
			}
		}

		if regionUrl != "" && !regionConnected {
			clients[common.REGION_CTX], err = ethclient.Dial(regionUrl)
			if err != nil {
				if !regionErrorPrinted {
					log.Println("Unable to connect to node:", "Region", regionUrl)
					regionErrorPrinted = true
				}
			} else {
				regionConnected = true
				log.Println("Connected to Region at: ", regionUrl)
			}
		}

		if zoneUrl != "" && !zoneConnected {
			clients[common.ZONE_CTX], err = ethclient.Dial(zoneUrl)
			if err != nil {
				if !zoneErrorPrinted {
					log.Println("Unable to connect to node:", "Zone", zoneUrl)
					zoneErrorPrinted = true
				}
			} else {
				zoneConnected = true
				log.Println("Connected to Zone at: ", zoneUrl)
			}
		}
	}
	return clients
}

func (s *ProxyServer) Start() {
	log.Printf("Starting proxy on %v", s.config.Proxy.Listen)
	r := mux.NewRouter()
	srv := &http.Server{
		Addr:           s.config.Proxy.Listen,
		Handler:        r,
		MaxHeaderBytes: s.config.Proxy.LimitHeadersSize,
	}

	if _, err := s.clients[common.ZONE_CTX].SubscribePendingHeader(context.Background(), s.updateCh); err != nil {
		log.Fatal("Failed to subscribe to pending header events: ", err)
	}

	err := srv.ListenAndServe()
	if err != nil {
		log.Fatalf("Failed to start proxy: %v", err)
	}
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

func (s *ProxyServer) fetchBlockTemplate() {
	pendingHeader, err := s.clients[common.ZONE_CTX].GetPendingHeader(context.Background())
	if err != nil {
		log.Printf("Error while getting pending header (work) on %s: %s", (*s.upstreams)[common.ZONE_CTX].Name, err)
		return
	}
	s.updateBlockTemplate(pendingHeader)
}

func (s *ProxyServer) updateBlockTemplate(pendingHeader *types.Header) {
	t := s.currentBlockTemplate()

	// Short circuit if the pending header is the same as the current one
	if t != nil && t.Header != nil && t.Header.SealHash() == pendingHeader.SealHash() {
		return
	}
	newTemplate := BlockTemplate{
		Header: pendingHeader,
		Target: consensus.DifficultyToTarget(pendingHeader.Difficulty()),
		Height: pendingHeader.NumberArray(),
	}

	if t == nil {
		newTemplate.JobID = 0
	} else {
		newTemplate.JobID = t.JobID + 1
	}

	s.blockTemplate.Store(&newTemplate)
	s.headerCache.Add(newTemplate.JobID, newTemplate.Header)
	log.Printf("New block to mine on %s at height %d", common.OrderToString(common.ZONE_CTX), pendingHeader.NumberArray())
	log.Printf("Sealhash: %#x", pendingHeader.SealHash())

	go s.broadcastNewJobs()
}

func (s *ProxyServer) verifyMinedHeader(jobID uint, nonce []byte) (*types.Header, error) {
	header, ok := s.headerCache.Get(jobID)
	if !ok {
		return nil, fmt.Errorf("Unable to find header for that jobID: %d", jobID)
	}
	header = types.CopyHeader(header)

	header.SetNonce(types.BlockNonce(nonce))
	mixHash, _ := s.engine.ComputePowLight(header)
	header.SetMixHash(mixHash)

	if header.NumberU64(common.ZONE_CTX) != s.currentBlockTemplate().Header.NumberU64(common.ZONE_CTX) {
		log.Printf("Stale header received, block number: %d", header.NumberU64(common.ZONE_CTX))
	}

	powHash, err := s.engine.VerifySeal(header)
	log.Printf("Miner submitted a block. Number: %d. Blockhash: %#x", header.NumberU64(common.ZONE_CTX), header.Hash())
	if err != nil {
		return nil, fmt.Errorf("unable to verify seal of block: %#x. %v", powHash, err)
	}

	return header, nil
}

func (s *ProxyServer) submitMinedHeader(cs *Session, header *types.Header) error {

	_, order, err := s.engine.CalcOrder(header)
	if err != nil {
		return fmt.Errorf("rejecting header: %v", err)
	}

	log.Printf("Received a %s block", strings.ToLower(common.OrderToString(order)))

	// Send mined header to the relevant go-quai nodes.
	// Should be synchronous starting with the lowest levels.
	for i := common.HierarchyDepth - 1; i >= order; i-- {
		err := s.clients[i].ReceiveMinedHeader(context.Background(), header)
		if err != nil {
			// Header was rejected. Refresh workers to try again.
			cs.pushNewJob(s.currentBlockTemplate())
			return fmt.Errorf("rejected header: %v", err)
		}
	}

	return nil
}
