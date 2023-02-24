package rpc

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"math/big"
	"net/http"

	"log"
	"sync"

	"github.com/INFURA/go-ethlibs/jsonrpc"
	"github.com/dominant-strategies/go-quai/common"
	"github.com/dominant-strategies/go-quai/common/hexutil"
	"github.com/dominant-strategies/go-quai/core/types"
	"github.com/dominant-strategies/go-quai/quaiclient"

	"github.com/dominant-strategies/go-quai-stratum/util"
)

type RPCClient struct {
	sync.RWMutex
	Url         string
	Name        string
	sick        bool
	sickRate    int
	successRate int
	client      *http.Client
}

type GetBlockReply struct {
	ParentHash    []common.Hash    `json:"parentHash"          gencodec:"required"`
	UncleHash     []common.Hash    `json:"sha3Uncles"          gencodec:"required"`
	Coinbase      []common.Address `json:"miner"               gencodec:"required"`
	Root          []common.Hash    `json:"stateRoot"           gencodec:"required"`
	TxHash        []common.Hash    `json:"transactionsRoot"    gencodec:"required"`
	EtxHash       []common.Hash    `json:"extTransactionsRoot" gencodec:"required"`
	EtxRollupHash []common.Hash    `json:"extRollupRoot"       gencodec:"required"`
	ManifestHash  []common.Hash    `json:"manifestHash"        gencodec:"required"`
	ReceiptHash   []common.Hash    `json:"receiptsRoot"        gencodec:"required"`
	Bloom         []types.Bloom    `json:"logsBloom"           gencodec:"required"`
	Difficulty    []big.Int        `json:"difficulty"          gencodec:"required"`
	Number        []big.Int        `json:"number"              gencodec:"required"`
	GasLimit      []hexutil.Uint64 `json:"gasLimit"            gencodec:"required"`
	GasUsed       []hexutil.Uint64 `json:"gasUsed"             gencodec:"required"`
	BaseFee       []*hexutil.Big   `json:"baseFeePerGas"       gencodec:"required"`
	Location      common.Location  `json:"location"            gencodec:"required"`
	Time          hexutil.Uint64   `json:"timestamp"           gencodec:"required"`
	Extra         hexutil.Bytes    `json:"extraData"           gencodec:"required"`
	Nonce         types.BlockNonce `json:"nonce"`
	Hash          common.Hash      `json:"hash"`
}

const receiptStatusSuccessful = "0x1"

type TxReceipt struct {
	TxHash    string `json:"transactionHash"`
	GasUsed   string `json:"gasUsed"`
	BlockHash string `json:"blockHash"`
	Status    string `json:"status"`
}

func (r *TxReceipt) Confirmed() bool {
	return len(r.BlockHash) > 0
}

// Use with previous method
func (r *TxReceipt) Successful() bool {
	if len(r.Status) > 0 {
		return r.Status == receiptStatusSuccessful
	}
	return true
}

type Tx struct {
	Gas      string `json:"gas"`
	GasPrice string `json:"gasPrice"`
	Hash     string `json:"hash"`
}

type JSONRpcResp struct {
	Id     *json.RawMessage       `json:"id"`
	Result *json.RawMessage       `json:"result"`
	Error  map[string]interface{} `json:"error"`
}

func NewRPCClient(name, url, timeout string) *RPCClient {
	rpcClient := &RPCClient{Name: name, Url: url}
	timeoutIntv := util.MustParseDuration(timeout)
	rpcClient.client = &http.Client{
		Timeout: timeoutIntv,
	}
	return rpcClient
}

func (r *RPCClient) GetWork() (*types.Header, error) {
	rpcResp, err := r.doPost(r.Url, "quai_getPendingHeader", nil)
	if err != nil {
		log.Fatalf("Unable to post data while getting pending header: %v", err)
		return nil, err
	}
	var reply *types.Header
	err = json.Unmarshal(*rpcResp.Result, &reply)
	return reply, err
}

func (r *RPCClient) SubmitMinedHeader(mined_header *types.Header) error {
	header_msg := quaiclient.RPCMarshalHeader(mined_header)
	_, err := r.doPost(r.Url, "quai_receiveMinedHeader", header_msg)

	return err
}

func (r *RPCClient) GetBlockByHeight(height int64) (*types.Header, error) {
	params := []interface{}{fmt.Sprintf("0x%x", height)}
	return r.getBlockBy("quai_getHeaderByNumber", params)
}

func (r *RPCClient) GetBlockByHash(hash string) (*types.Header, error) {
	params := []interface{}{hash, true}
	return r.getBlockBy("eth_getBlockByHash", params)
}

func (r *RPCClient) GetUncleByBlockNumberAndIndex(height int64, index int) (*types.Header, error) {
	params := []interface{}{fmt.Sprintf("0x%x", height), fmt.Sprintf("0x%x", index)}
	return r.getBlockBy("eth_getUncleByBlockNumberAndIndex", params)
}

func (r *RPCClient) getBlockBy(method string, params []interface{}) (*types.Header, error) {
	rpcResp, err := r.doPost(r.Url, method, params)
	if err != nil {
		return nil, err
	}
	if rpcResp.Result != nil {
		var reply *types.Header
		err = json.Unmarshal(*rpcResp.Result, &reply)
		return reply, err
	}
	return nil, nil
}

func (r *RPCClient) doPost(url string, method string, params interface{}) (*JSONRpcResp, error) {
	var data []byte
	var err error
	if method == "quai_receiveMinedHeader" {
		jsonReq, err := jsonrpc.MakeRequest(0, method, params)
		if err != nil {
			log.Fatalf("Unable to make new rpc request to go-quai: %v", err)
			return nil, err
		}
		data, err = jsonReq.MarshalJSON()
	} else {
		jsonReq := map[string]interface{}{"jsonrpc": "2.0", "method": method, "params": params, "id": 0}
		data, err = json.Marshal(jsonReq)
	}

	if err != nil {
		log.Fatalf("Unable to marshal rpc request into JSON: %v", err)
		return nil, err
	}

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(data))
	if err != nil {
		log.Fatalf("Error while posting data to go-quai: %v", err)
		return nil, err
	}
	req.Header.Set("Content-Length", (string)(len(data)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := r.client.Do(req)
	if err != nil {
		r.markSick()
		return nil, err
	}
	defer resp.Body.Close()

	var rpcResp *JSONRpcResp
	err = json.NewDecoder(resp.Body).Decode(&rpcResp)
	if err != nil {
		r.markSick()
		return nil, err
	}
	if rpcResp.Error != nil {
		r.markSick()
		return nil, errors.New(rpcResp.Error["message"].(string))
	}
	return rpcResp, err
}

func (r *RPCClient) Check() bool {
	_, err := r.GetWork()
	if err != nil {
		return false
	}
	r.markAlive()
	return !r.Sick()
}

func (r *RPCClient) Sick() bool {
	r.RLock()
	defer r.RUnlock()
	return r.sick
}

func (r *RPCClient) markSick() {
	r.Lock()
	r.sickRate++
	r.successRate = 0
	if r.sickRate >= 5 {
		r.sick = true
	}
	r.Unlock()
}

func (r *RPCClient) markAlive() {
	r.Lock()
	r.successRate++
	if r.successRate >= 5 {
		r.sick = false
		r.sickRate = 0
		r.successRate = 0
	}
	r.Unlock()
}
