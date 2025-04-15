package proxy

import (
	"github.com/dominant-strategies/go-quai-stratum/api"
	"github.com/dominant-strategies/go-quai-stratum/policy"
	"github.com/dominant-strategies/go-quai-stratum/storage"
	"github.com/dominant-strategies/go-quai/common"
	"github.com/dominant-strategies/go-quai/common/hexutil"
)

type Config struct {
	Name                  string        `json:"name"`
	Proxy                 Proxy         `json:"proxy"`
	Mining                Mining        `json:"mining"`
	Api                   api.ApiConfig `json:"api"`
	Upstream              []Upstream    `json:"upstream"`
	UpstreamCheckInterval string        `json:"upstreamCheckInterval"`

	Threads int `json:"threads"`

	Network string         `json:"network"`
	Coin    string         `json:"coin"`
	Redis   storage.Config `json:"redis"`

	AvgBlockTime    float64 `json:"avgBlockTime"`
	BlockTimeWindow int64   `json:"blockTimeWindow"`

	NewrelicName    string `json:"newrelicName"`
	NewrelicKey     string `json:"newrelicKey"`
	NewrelicVerbose bool   `json:"newrelicVerbose"`
	NewrelicEnabled bool   `json:"newrelicEnabled"`
}

type Proxy struct {
	Enabled              bool         `json:"enabled"`
	Listen               string       `json:"listen"`
	LimitHeadersSize     int          `json:"limitHeadersSize"`
	LimitBodySize        int64        `json:"limitBodySize"`
	BehindReverseProxy   bool         `json:"behindReverseProxy"`
	BlockRefreshInterval string       `json:"blockRefreshInterval"`
	Difficulty           *hexutil.Big `json:"difficulty"`
	StateUpdateInterval  string       `json:"stateUpdateInterval"`
	HashrateExpiration   string       `json:"hashrateExpiration"`

	SealMining      bool           `json:"sealMining"`
	QuaiCoinbase    common.Address `json:"quaiCoinbase"`
	QiCoinbase      common.Address `json:"qiCoinbase"`
	Lockup          uint8          `json:"lockup"`
	MinerPreference float64        `json:"minerPreference"`

	Policy policy.Config `json:"policy"`

	MaxFails    int64 `json:"maxFails"`
	HealthCheck bool  `json:"healthCheck"`

	Stratum Stratum `json:"stratum"`

	StratumNiceHash StratumNiceHash `json:"stratum_nice_hash"`
}

type Mining struct {
	GpuType string `json:"gpuType"`
	Enabled bool   `json:"enabled"`
}

type Stratum struct {
	Enabled bool   `json:"enabled"`
	Listen  string `json:"listen"`
	Timeout string `json:"timeout"`
	MaxConn int    `json:"maxConn"`
}

type StratumNiceHash struct {
	Enabled bool   `json:"enabled"`
	Listen  string `json:"listen"`
	Timeout string `json:"timeout"`
	MaxConn int    `json:"maxConn"`
}

type Upstream struct {
	Name    string `json:"name"`
	Url     string `json:"url"`
	Timeout string `json:"timeout"`
}
