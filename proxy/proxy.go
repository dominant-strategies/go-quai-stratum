package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"math/rand"
	"net"
	"net/http"
	"strconv"
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
	"github.com/dominant-strategies/go-quai/log"
	"github.com/dominant-strategies/go-quai/params"
	"github.com/dominant-strategies/go-quai/quaiclient"
	"google.golang.org/protobuf/proto"

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
	updateCh chan []byte

	// Keep track of previous headers
	woCache *lru.LRU[uint, *types.WorkObject]

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

type SliceClients [common.HierarchyDepth]*quaiclient.Client

func NewProxy(cfg *Config, backend *storage.RedisClient) *ProxyServer {
	if len(cfg.Name) == 0 {
		log.Global.Fatal("You must set instance name")
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
			log.Global,
		),
		updateCh: make(chan []byte, 5*1024),
		woCache:  lru.NewLRU[uint, *types.WorkObject](10, nil, 0),
	}
	proxy.diff = util.GetTargetHex(cfg.Proxy.Difficulty)

	proxy.clients = proxy.connectToSlice()

	proxy.rng = rand.New(rand.NewSource(time.Now().UnixNano()))

	proxy.hashrateExpiration = util.MustParseDuration(cfg.Proxy.HashrateExpiration)

	refreshIntv := util.MustParseDuration(cfg.Proxy.BlockRefreshInterval)
	refreshTimer := time.NewTimer(refreshIntv)
	log.Global.Printf("Set block refresh every %v", refreshIntv)

	proxy.fetchBlockTemplate()

	if cfg.Proxy.Stratum.Enabled {
		proxy.sessions = make(map[*Session]struct{})
		go proxy.ListenTCP()
	}

	go func() {
		for {
			select {
			case <-refreshTimer.C:
				proxy.fetchBlockTemplate()
				refreshTimer.Reset(refreshIntv)
			case newPendingHeader := <-proxy.updateCh:
				if len(newPendingHeader) > 0 {
					protoWo := &types.ProtoWorkObject{}
					err := proto.Unmarshal(newPendingHeader, protoWo)
					if err != nil {
						log.Global.Error("Error unmarshalling new pending header", "err", err)
						continue
					}
					pendingHeader := &types.WorkObject{}
					location, err := util.LocationFromName((*proxy.upstreams)[common.ZONE_CTX].Name)
					if err != nil {
						log.Global.WithFields(log.Fields{
							"locationName": (*proxy.upstreams)[common.ZONE_CTX].Name,
							"err":          err,
						}).Error("Error getting location from name")
						continue
					}
					err = pendingHeader.ProtoDecode(protoWo, location, types.PEtxObject)
					if err != nil {
						log.Global.Error("Error decoding new pending header", "err", err)
						continue
					}
					proxy.updateBlockTemplate(pendingHeader)
				}
			}
		}
	}()

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
		log.Global.Fatal("Please specify region port!")
	}
	zoneUrl := (*s.upstreams)[common.ZONE_CTX].Url
	if zoneUrl == "" {
		log.Global.Fatal("Please specify zone port!")
	}

	for !primeConnected || !regionConnected || !zoneConnected {
		if primeUrl != "" && !primeConnected {
			clients[common.PRIME_CTX], err = quaiclient.Dial(primeUrl, log.Global)
			if err != nil {
				if !primeErrorPrinted {
					log.Global.Println("Unable to connect to node:", "Prime", primeUrl)
					primeErrorPrinted = true
				}
			} else {
				primeConnected = true
				log.Global.Println("Connected to Prime at: ", primeUrl)
			}
		}

		if regionUrl != "" && !regionConnected {
			clients[common.REGION_CTX], err = quaiclient.Dial(regionUrl, log.Global)
			if err != nil {
				if !regionErrorPrinted {
					log.Global.Println("Unable to connect to node:", "Region", regionUrl)
					regionErrorPrinted = true
				}
			} else {
				regionConnected = true
				log.Global.Println("Connected to Region at: ", regionUrl)
			}
		}

		if zoneUrl != "" && !zoneConnected {
			clients[common.ZONE_CTX], err = quaiclient.Dial(zoneUrl, log.Global)
			if err != nil {
				if !zoneErrorPrinted {
					log.Global.Println("Unable to connect to node:", "Zone", zoneUrl)
					zoneErrorPrinted = true
				}
			} else {
				zoneConnected = true
				log.Global.Println("Connected to Zone at: ", zoneUrl)
			}
		}
	}
	return clients
}

func (s *ProxyServer) Start() {
	log.Global.Printf("Starting proxy on %v", s.config.Proxy.Listen)
	r := mux.NewRouter()
	srv := &http.Server{
		Addr:           s.config.Proxy.Listen,
		Handler:        r,
		MaxHeaderBytes: s.config.Proxy.LimitHeadersSize,
	}

	if _, err := s.clients[common.ZONE_CTX].SubscribePendingHeader(context.Background(), s.updateCh); err != nil {
		log.Global.Fatal("Failed to subscribe to pending header events: ", err)
	}

	err := srv.ListenAndServe()
	if err != nil {
		log.Global.Fatalf("Failed to start proxy: %v", err)
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
		log.Global.Printf("Error while getting pending header (work) on %s: %s", (*s.upstreams)[common.ZONE_CTX].Name, err)
		return
	}
	s.updateBlockTemplate(pendingHeader)
}

func (s *ProxyServer) updateBlockTemplate(pendingWo *types.WorkObject) {
	t := s.currentBlockTemplate()

	// Short circuit if the pending header is the same as the current one
	if t != nil && t.WorkObject != nil && t.WorkObject.WorkObjectHeader() != nil && t.WorkObject.WorkObjectHeader().SealHash() == pendingWo.SealHash() {
		return
	}

	threshold, err := consensus.CalcWorkShareThreshold(pendingWo.WorkObjectHeader(), getWorkshareThresholdDiff(int(pendingWo.NumberU64(common.ZONE_CTX))))
	if err != nil {
		log.Global.WithField("err", err).Error("Error calculating the target")
		return
	}
	newTemplate := BlockTemplate{
		WorkObject: pendingWo,
		Target:     threshold,
		Height:     pendingWo.NumberArray(),
	}

	if t == nil {
		newTemplate.JobID = 0
	} else {
		newTemplate.JobID = t.JobID + 1
	}

	s.blockTemplate.Store(&newTemplate)
	s.woCache.Add(newTemplate.JobID, newTemplate.WorkObject)
	log.Global.Printf("New block to mine on %s at height %d", s.config.Upstream[common.ZONE_CTX].Name, pendingWo.NumberArray())

	difficultyMh := strconv.FormatUint(new(big.Int).Div(consensus.TargetToDifficulty(newTemplate.Target), big.NewInt(1000)).Uint64(), 10)
	if len(difficultyMh) >= 3 {
		log.Global.Printf("Workshare difficulty: %s.%s Mh", difficultyMh[:len(difficultyMh)-3], difficultyMh[len(difficultyMh)-3:])
	}
	log.Global.Printf("Sealhash: %#x", pendingWo.SealHash())

	go s.broadcastNewJobs()
}

func (s *ProxyServer) verifyMinedHeader(jobID uint, nonce []byte) (*types.WorkObject, error) {
	wObject, ok := s.woCache.Get(jobID)
	if !ok {
		return nil, fmt.Errorf("unable to find header for that jobID: %d", jobID)
	}
	wObject = types.CopyWorkObject(wObject)

	wObject.WorkObjectHeader().SetNonce(types.BlockNonce(nonce))
	mixHash, _ := s.engine.ComputePowLight(wObject.WorkObjectHeader())
	wObject.SetMixHash(mixHash)

	if wObject.NumberU64(common.ZONE_CTX) != s.currentBlockTemplate().WorkObject.NumberU64(common.ZONE_CTX) {
		log.Global.Printf("Stale header received, block number: %d", wObject.NumberU64(common.ZONE_CTX))
	}

	err := s.clients[common.ZONE_CTX].ReceiveWorkShare(context.Background(), wObject.WorkObjectHeader())
	if err != nil {
		return nil, err
	}

	return wObject, nil
}

func (s *ProxyServer) submitMinedHeader(cs *Session, wObject *types.WorkObject) error {

	powHash, err := s.engine.VerifySeal(wObject.WorkObjectHeader())
	if err != nil {
		return fmt.Errorf("unable to verify seal of block: %#x. %v", powHash, err)
	}

	order, err := (*s.clients[common.ZONE_CTX]).CalcOrder(context.Background(), wObject)
	if err != nil {
		return fmt.Errorf("rejecting header: %v", err)
	}

	log.Global.Printf("Received a %s block", strings.ToLower(common.OrderToString(order)))

	// Send mined header to the relevant go-quai nodes.
	// Should be synchronous starting with the lowest levels.
	for i := common.HierarchyDepth - 1; i >= order; i-- {
		err := s.clients[i].ReceiveMinedHeader(context.Background(), wObject)
		if err != nil {
			// Header was rejected. Refresh workers to try again.
			cs.pushNewJob(s.currentBlockTemplate())
			return fmt.Errorf("rejected header: %v", err)
		}
	}

	return nil
}

func getWorkshareThresholdDiff(blockNumber int) int {
	if blockNumber < params.GoldenAgeForkNumberV1 {
		return params.OldWorkSharesThresholdDiff
	} else {
		return params.NewWorkSharesThresholdDiff
	}
}
