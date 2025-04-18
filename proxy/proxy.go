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
	"sync"
	"sync/atomic"
	"time"

	quai "github.com/dominant-strategies/go-quai"
	"github.com/dominant-strategies/go-quai-stratum/policy"
	"github.com/dominant-strategies/go-quai-stratum/storage"
	"github.com/dominant-strategies/go-quai-stratum/util"
	"github.com/dominant-strategies/go-quai/common"
	"github.com/dominant-strategies/go-quai/consensus"
	"github.com/dominant-strategies/go-quai/consensus/progpow"
	"github.com/dominant-strategies/go-quai/core/types"
	"github.com/dominant-strategies/go-quai/log"
	"github.com/dominant-strategies/go-quai/quaiclient"
	"google.golang.org/protobuf/proto"

	"github.com/gorilla/mux"
	lru "github.com/hashicorp/golang-lru/v2/expirable"
)

type ProxyServer struct {
	context            context.Context
	config             *Config
	blockTemplate      atomic.Value
	upstream           *Upstream
	clients            SliceClients
	backend            *storage.RedisClient
	diff               string
	threshold          uint64
	policy             *policy.PolicyServer
	hashrateExpiration time.Duration
	failsCount         int64
	engine             consensus.Engine
	rng                *rand.Rand

	// Channel to receive header updates
	updateCh   chan []byte
	customWoCh chan *quai.WorkShareUpdate

	// Keep track of previous headers
	woCache *lru.LRU[uint, workEntry]

	// Stratum
	sessionsMu sync.RWMutex
	sessions   map[*Session]struct{}
	timeout    time.Duration
	Extranonce string
}
type Session struct {
	ip         string
	port       string
	enc        *json.Encoder
	sealMining bool // true if the client is mining via only sealHashes (decentralized pool)

	// Stratum
	sync.Mutex
	conn       *net.TCPConn
	login      string
	Extranonce string
}

type SliceClients [common.HierarchyDepth]*quaiclient.Client

type workEntry struct {
	sealHash common.Hash
	wo       *types.WorkObject
}

func NewProxy(cfg *Config, backend *storage.RedisClient) *ProxyServer {
	if len(cfg.Name) == 0 {
		log.Global.Fatal("You must set instance name")
	}
	policy := policy.Start(&cfg.Proxy.Policy, backend)

	proxy := &ProxyServer{
		context:  context.Background(),
		config:   cfg,
		upstream: &cfg.Upstream,
		backend:  backend,
		policy:   policy,
		engine: progpow.New(
			progpow.Config{
				NotifyFull:   true,
				NodeLocation: common.Location{0, 0}},
			nil,
			false,
			log.Global,
		),
		woCache: lru.NewLRU[uint, workEntry](100, nil, 0),
	}

	if cfg.Proxy.SealMining {
		proxy.customWoCh = make(chan *quai.WorkShareUpdate, 100)
	} else {
		proxy.updateCh = make(chan []byte, 5*1024)
	}

	proxy.diff = util.GetTargetHex(cfg.Proxy.Difficulty)

	proxy.clients = proxy.connectToSlice()

	proxy.rng = rand.New(rand.NewSource(time.Now().UnixNano()))

	proxy.hashrateExpiration = util.MustParseDuration(cfg.Proxy.HashrateExpiration)

	refreshIntv := util.MustParseDuration(cfg.Proxy.BlockRefreshInterval)
	refreshTimer := time.NewTimer(refreshIntv)
	log.Global.Printf("Set block refresh every %v", refreshIntv)

	proxy.threshold = proxy.clients[common.ZONE_CTX].GetWorkShareP2PThreshold(proxy.context)

	if !cfg.Proxy.SealMining {
		// The node will only provide the workobject if SealMining is not enabled.
		proxy.fetchBlockTemplate()
	}

	if cfg.Proxy.Stratum.Enabled {
		proxy.sessions = make(map[*Session]struct{})
		go proxy.ListenTCP()
	}

	go func() {
		for {
			select {
			case <-refreshTimer.C:
				if !cfg.Proxy.SealMining {
					proxy.fetchBlockTemplate()
					refreshTimer.Reset(refreshIntv)
				}
			case newPendingHeader := <-proxy.updateCh:
				if len(newPendingHeader) > 0 {
					protoWo := &types.ProtoWorkObject{}
					err := proto.Unmarshal(newPendingHeader, protoWo)
					if err != nil {
						log.Global.Error("Error unmarshalling new pending header", "err", err)
						continue
					}
					pendingHeader := &types.WorkObject{}
					location, err := util.LocationFromName(proxy.upstream.Name)
					if err != nil {
						log.Global.WithFields(log.Fields{
							"locationName": proxy.upstream.Name,
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
			case customWo := <-proxy.customWoCh:
				proxy.updateCustomSealTemplate(customWo)

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
	zoneConnected := false
	zoneErrorPrinted := false

	zoneUrl := s.upstream.Url
	if zoneUrl == "" {
		log.Global.Fatal("Please specify zone port!")
	}

	// Zone must always be connected
	for !zoneConnected {
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

	if s.config.Proxy.SealMining {
		crit := quai.WorkShareCriteria{
			QuaiCoinbase:    s.config.Proxy.QuaiCoinbase,
			QiCoinbase:      s.config.Proxy.QiCoinbase,
			MinerPreference: s.config.Proxy.MinerPreference,
			LockupByte:      s.config.Proxy.Lockup,
		}
		if _, err := s.clients[common.ZONE_CTX].SubscribeCustomSealHash(s.context, crit, s.customWoCh); err != nil {
			log.Global.Fatal("Failed to subscribe to custom seal hash events: ", err)
		}
	} else {
		if _, err := s.clients[common.ZONE_CTX].SubscribePendingHeader(s.context, s.updateCh); err != nil {
			log.Global.Fatal("Failed to subscribe to pending header events: ", err)
		}
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

func (s *ProxyServer) fetchBlockTemplate() {
	pendingHeader, err := s.clients[common.ZONE_CTX].GetPendingHeader(s.context)
	if err != nil {
		log.Global.Printf("Error while getting pending header (work) on %s: %s", s.upstream.Name, err)
		return
	}
	s.updateBlockTemplate(pendingHeader)
}

func (s *ProxyServer) updateBlockTemplate(pendingWo *types.WorkObject) {
	t := s.currentBlockTemplate()

	// Short circuit if the pending header is the same as the current one
	if t != nil && t.WorkObject != nil && t.WorkObject.WorkObjectHeader() != nil && t.WorkObject.Time() == pendingWo.Time() {
		return
	}

	// Modify the header coinbase to be the miner's address.
	newWo := types.CopyWorkObjectHeader(pendingWo.WorkObjectHeader())
	newWo.PickCoinbase(s.config.Proxy.MinerPreference, s.config.Proxy.QuaiCoinbase, s.config.Proxy.QiCoinbase)
	newWo.SetLock(s.config.Proxy.Lockup)

	if newWo.PrimeTerminusNumber() == nil {
		// Return rather than crashing if the header is not yet available.
		return
	}

	threshold, err := consensus.CalcWorkShareThreshold(newWo.Difficulty(), int(s.threshold))
	if err != nil {
		log.Global.WithField("err", err).Error("Error calculating the target")
		return
	}
	newTemplate := &BlockTemplate{
		WorkObject: pendingWo,
		Target:     threshold,
		Height:     pendingWo.NumberArray(),
		CustomSeal: pendingWo.SealHash(),
	}

	s.finalizeTemplate(newTemplate)

	difficultyMh := strconv.FormatUint(new(big.Int).Div(consensus.TargetToDifficulty(newTemplate.Target), big.NewInt(1000)).Uint64(), 10)

	if len(difficultyMh) >= 3 {
		log.Global.WithField(
			"difficulty", difficultyMh[:len(difficultyMh)-3]+"."+difficultyMh[len(difficultyMh)-3:]+"Mh",
		).Info("Workshare difficulty")
	}
}

func (s *ProxyServer) updateCustomSealTemplate(workShareUpdate *quai.WorkShareUpdate) {
	threshold, err := consensus.CalcWorkShareThreshold(workShareUpdate.Difficulty, int(s.threshold))
	if err != nil {
		log.Global.WithField("err", err).Error("Error with the provided workshare difficulty or threshold")
	}

	newTemplate := &BlockTemplate{
		CustomSeal:          workShareUpdate.SealHash,
		Target:              threshold,
		PrimeTerminusNumber: workShareUpdate.PrimeTerminusNumber,
	}

	s.finalizeTemplate(newTemplate)
}

func (s *ProxyServer) finalizeTemplate(newTemplate *BlockTemplate) {
	currentTemplate := s.currentBlockTemplate()

	if currentTemplate == nil {
		newTemplate.JobID = 0
	} else {
		newTemplate.JobID = currentTemplate.JobID + 1
	}

	s.blockTemplate.Store(newTemplate)
	s.woCache.Add(newTemplate.JobID, workEntry{
		sealHash: newTemplate.CustomSeal,
		wo:       newTemplate.WorkObject,
	})

	if !s.config.Proxy.SealMining {
		log.Global.WithFields(log.Fields{
			"location": s.config.Upstream.Name,
			"number":   newTemplate.WorkObject.NumberArray(),
			"sealHash": newTemplate.WorkObject.SealHash(),
		}).Printf("New block to mine")
	} else {
		log.Global.WithFields(log.Fields{
			"location": s.config.Upstream.Name,
			"sealHash": newTemplate.CustomSeal,
		}).Printf("New block to mine")
	}

	go s.broadcastNewJobs()
}

func (s *ProxyServer) receiveNonce(jobID uint, nonce types.BlockNonce) error {
	workEntry, ok := s.woCache.Get(jobID)
	if !ok {
		return fmt.Errorf("unable to find header for that jobID: %d", jobID)
	}

	return s.clients[common.ZONE_CTX].ReceiveNonce(s.context, workEntry.sealHash, nonce)
}

func (s *ProxyServer) verifyMinedHeader(jobID uint, nonce []byte) (*types.WorkObject, error) {
	workEntry, ok := s.woCache.Get(jobID)
	if !ok {
		return nil, fmt.Errorf("unable to find header for that jobID: %d", jobID)
	}

	wObject := workEntry.wo
	wObject = types.CopyWorkObject(wObject)

	wObject.WorkObjectHeader().SetNonce(types.BlockNonce(nonce))
	mixHash, _ := s.engine.ComputePowLight(wObject.WorkObjectHeader())
	wObject.SetMixHash(mixHash)

	if wObject.NumberU64(common.ZONE_CTX) != s.currentBlockTemplate().WorkObject.NumberU64(common.ZONE_CTX) {
		log.Global.Printf("Stale header received, block number: %d", wObject.NumberU64(common.ZONE_CTX))
	}

	err := s.clients[common.ZONE_CTX].ReceiveNonce(s.context, workEntry.sealHash, types.BlockNonce(nonce))

	return wObject, err
}

func (s *ProxyServer) submitMinedHeader(cs *Session, wObject *types.WorkObject) error {

	return s.clients[common.ZONE_CTX].ReceiveMinedHeader(s.context, wObject)
}
