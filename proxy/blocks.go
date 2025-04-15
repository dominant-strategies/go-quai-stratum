package proxy

import (
	"math/big"

	"sync"

	"github.com/dominant-strategies/go-quai/common"
	"github.com/dominant-strategies/go-quai/core/types"
)

type BlockTemplate struct {
	sync.RWMutex
	WorkObject          *types.WorkObject
	Target              *big.Int
	Difficulty          *big.Int
	Height              []*big.Int
	PrimeTerminusNumber *big.Int

	CustomSeal common.Hash // Used for decentralized mining pools where the full workObject is not provided.
	JobID      uint
}
