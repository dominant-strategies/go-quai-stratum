package proxy

import (
	"errors"
	"log"
	"math/big"

	"sync"

	"github.com/dominant-strategies/go-quai/common"
	"github.com/dominant-strategies/go-quai/core/types"

	"github.com/dominant-strategies/go-quai/common/hexutil"
)

const maxBacklog = 3

type BlockTemplate struct {
	sync.RWMutex
	Header     *types.Header
	Target     *big.Int
	Difficulty *big.Int
	Height     []*big.Int
	nonces     map[string]bool
}

type Block struct {
	difficulty  []*hexutil.Big
	hashNoNonce common.Hash
	nonce       uint64
	number      uint64
}

func (b Block) Difficulty() []*hexutil.Big { return b.difficulty }
func (b Block) HashNoNonce() common.Hash   { return b.hashNoNonce }
func (b Block) Nonce() uint64              { return b.nonce }
func (b Block) NumberU64() uint64          { return b.number }

func (s *ProxyServer) fetchBlockTemplate() {
	rpc := s.rpc(common.ZONE_CTX)
	t := s.currentBlockTemplate()
	pendingHeader, err := rpc.GetWork()
	if err != nil {
		log.Printf("Error while getting pending header (work) on %s: %s", rpc.Name, err)
		return
	}
	// No need to update, we have fresh job
	if t != nil && t.Header == pendingHeader {
		return
	} else if t != nil {
		t.Header = pendingHeader
	}

	newTemplate := BlockTemplate{
		Header:     pendingHeader,
		Target:     pendingHeader.DifficultyArray()[common.ZONE_CTX],
		Height:     pendingHeader.NumberArray(),
		Difficulty: pendingHeader.DifficultyArray()[common.ZONE_CTX],
	}

	s.blockTemplate.Store(&newTemplate)
	log.Printf("New block to mine on %s at height %d", rpc.Name, pendingHeader.NumberArray())

	if s.config.Proxy.Stratum.Enabled {
		go s.broadcastNewJobs()
	}
}

var (
	big2e256 = new(big.Int).Exp(big.NewInt(2), big.NewInt(256), big.NewInt(0)) // 2^256
)

// This function determines the difficulty order of a block
func GetDifficultyOrder(header *types.Header) (int, error) {
	if header == nil {
		return common.HierarchyDepth, errors.New("no header provided")
	}
	blockhash := header.Hash()
	for i, difficulty := range header.DifficultyArray() {
		if difficulty != nil && big.NewInt(0).Cmp(difficulty) < 0 {
			target := new(big.Int).Div(big2e256, difficulty)
			if new(big.Int).SetBytes(blockhash.Bytes()).Cmp(target) <= 0 {
				return i, nil
			}
		}
	}
	return -1, errors.New("block does not satisfy minimum difficulty")
}
