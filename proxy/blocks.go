package proxy

import (
	"math/big"

	"sync"

	"github.com/dominant-strategies/go-quai/common"
	"github.com/dominant-strategies/go-quai/core/types"

	"github.com/dominant-strategies/go-quai/common/hexutil"
)

type BlockTemplate struct {
	sync.RWMutex
	Header     *types.Header
	Target     *big.Int
	Difficulty *big.Int
	Height     []*big.Int
	JobID      uint
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
