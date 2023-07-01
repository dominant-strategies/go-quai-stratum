package proxy

import (
	"fmt"
	"log"
	"regexp"
	"strings"

	"github.com/dominant-strategies/go-quai/common"
	"github.com/dominant-strategies/go-quai/core/types"
)

// Allow only lowercase hexadecimal with 0x prefix
var noncePattern = regexp.MustCompile("^0x[0-9a-f]{16}$")
var hashPattern = regexp.MustCompile("^0x[0-9a-f]{64}$")
var workerPattern = regexp.MustCompile("^[0-9a-zA-Z-_]{1,8}$")

// Clients should provide a Quai address when logging in.
func (s *ProxyServer) handleLoginRPC(cs *Session, req Request) error {

	params, ok := req.Params.([]interface{})
	if !ok {
		return fmt.Errorf("login payload doesn't conform to stratum spec")
	}
	if len(params) == 0 {
		return fmt.Errorf("no login information provided")
	}

	account, ok := params[0].(string)
	if !ok {
		return fmt.Errorf("account info is not a string")
	}
	login := strings.ToLower(account)

	if !s.policy.ApplyLoginPolicy(login, cs.ip) {
		return fmt.Errorf("you are blacklisted")
	}
	cs.login = login
	s.registerSession(cs)
	log.Printf("Stratum miner connected %v@%v", login, cs.ip)

	if s.config.Proxy.Stratum.Enabled {
		// Provide the difficulty to the client. Must be completed before `mining.notify`.
		cs.setMining(common.BytesToHash(s.currentBlockTemplate().Target.Bytes()))
		go s.broadcastNewJobs()
	}

	return nil
}

// Returns the cached header to clients.
func (s *ProxyServer) handleGetWorkRPC(cs *Session) (*types.Header, *ErrorReply) {
	t := s.currentBlockTemplate()
	if t == nil || t.Header == nil || s.isSick() {
		return nil, &ErrorReply{Code: 0, Message: "Work not ready"}
	}
	return t.Header, nil
}
