package proxy

import (
	"bufio"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"strings"

	"github.com/dominant-strategies/go-quai-stratum/util"
	"github.com/dominant-strategies/go-quai/common"
	"github.com/dominant-strategies/go-quai/core/types"
)

const (
	c_Max_Req_Size = 4096
)

func (s *ProxyServer) ListenTCP() {
	timeout := util.MustParseDuration(s.config.Proxy.Stratum.Timeout)
	s.timeout = timeout

	addr, err := net.ResolveTCPAddr("tcp4", s.config.Proxy.Stratum.Listen)
	if err != nil {
		log.Fatalf("Error: %v", err)
	}
	server, err := net.ListenTCP("tcp4", addr)
	if err != nil {
		log.Fatalf("Error: %v", err)
	}
	defer server.Close()

	log.Printf("Stratum listening on %s", s.config.Proxy.Stratum.Listen)
	var accept = make(chan int, s.config.Proxy.Stratum.MaxConn)

	n := 0
	for {
		conn, err := server.AcceptTCP()
		if err != nil {
			continue
		}
		conn.SetKeepAlive(true)

		ip, _, _ := net.SplitHostPort(conn.RemoteAddr().String())

		if s.policy.IsBanned(ip) || !s.policy.ApplyLimitPolicy(ip) {
			conn.Close()
			continue
		}
		n += 1
		cs := &Session{conn: conn, ip: ip}

		accept <- n
		go func(cs *Session) {
			err = s.handleTCPClient(cs)
			if err != nil {
				s.removeSession(cs)
				conn.Close()
			}
			<-accept
		}(cs)
	}
}

type Request struct {
	Id     uint        `json:"id"`
	Method string      `json:"method"`
	Params interface{} `json:"params"`
}

type Response struct {
	ID     uint        `json:"id"`
	Result interface{} `json:"result"`
	Error  interface{} `json:"error"`
}

type Notification struct {
	Method string      `json:"method"`
	Params interface{} `json:"params"`
}

func (s *ProxyServer) handleTCPClient(cs *Session) error {
	cs.enc = json.NewEncoder(cs.conn)
	connbuff := bufio.NewReaderSize(cs.conn, c_Max_Req_Size)
	for {
		data, isPrefix, err := connbuff.ReadLine()
		if isPrefix {
			log.Printf("Socket flood detected from %s", cs.ip)
			s.policy.BanClient(cs.ip)
			cs.sendTCPError(err)
			s.removeSession(cs)
			return err
		} else if err == io.EOF {
			log.Printf("Client %s disconnected", cs.ip)
			cs.sendTCPError(err)
			s.removeSession(cs)
			break
		} else if err != nil {
			log.Printf("Error reading from socket: %v", err)
			cs.sendTCPError(err)
			return err
		}

		if len(data) > 1 {
			var req Request
			err := json.Unmarshal(data, &req)
			if err != nil {
				return fmt.Errorf("error decoding JSON: %v", err)
			}
			err = cs.handleTCPMessage(s, &req)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (cs *Session) handleTCPMessage(s *ProxyServer, req *Request) error {
	// Handle RPC methods
	switch req.Method {
	case "mining.hello":
		response := Response{
			ID: req.Id,
			Result: map[string]interface{}{
				"proto":     "EthereumStratum/2.0.0",
				"encoding":  "plain",
				"resume":    0,
				"timeout":   30,
				"maxerrors": 5,
				"node":      "go-quai/development",
			},
		}
		return cs.enc.Encode(&response)

	case "mining.bye":
		s.removeSession(cs)
		return nil

	case "mining.subscribe":
		response := Response{
			ID:     req.Id,
			Result: "s-12345",
		}
		return cs.enc.Encode(&response)

	case "mining.authorize":
		response := Response{
			ID:     req.Id,
			Result: "s-12345",
		}
		err := cs.enc.Encode(&response)
		if err != nil {
			log.Printf("Error encoding JSON: %v", err)
			return err
		}
		return s.handleLoginRPC(cs, *req)

	case "mining.submit":
		header := s.currentBlockTemplate().Header

		nonce, err := hex.DecodeString(req.Params.([]interface{})[1].(string))
		if err != nil {
			log.Printf("Error decoding nonce: %v", err)
			return err
		}

		header.SetNonce(types.BlockNonce(nonce))
		mixHash, _ := s.engine.ComputePowLight(header)
		header.SetMixHash(mixHash)

		err = s.submitMinedHeader(cs, header)
		if err != nil {
			log.Printf("Error submitting header: %v", err)
			return err
		}
		respones := Response{
			ID:     req.Id,
			Result: true,
		}
		return cs.enc.Encode(&respones)

	default:
		return nil
	}
}

func (cs *Session) sendTCPResult(id uint, result interface{}) error {
	cs.Lock()
	defer cs.Unlock()

	message := Response{
		ID:     id,
		Result: result,
		Error:  nil,
	}

	return cs.enc.Encode(&message)
}

func (cs *Session) sendTCPError(err error) {
	cs.Lock()
	defer cs.Unlock()

	message := Response{
		ID:     0,
		Result: nil,
		Error:  err,
	}

	cs.enc.Encode(&message)
}

func (cs *Session) pushNewJob(header *types.Header) error {
	cs.Lock()
	defer cs.Unlock()

	notification := Notification{
		Method: "mining.notify",
		Params: []string{
			fmt.Sprintf("%x", header.SealHash()),
			fmt.Sprintf("%x", header.Number(common.ZONE_CTX)),
			fmt.Sprintf("%x", header.SealHash()),
			"0",
		},
	}
	return cs.enc.Encode(&notification)
}

func (s *ProxyServer) registerSession(cs *Session) {
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	s.sessions[cs] = struct{}{}
}

func (s *ProxyServer) removeSession(cs *Session) {
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	delete(s.sessions, cs)
}

func (cs *Session) setMining(target common.Hash) error {
	cs.Lock()
	defer cs.Unlock()
	notification := Notification{
		Method: "mining.set",
		Params: map[string]interface{}{
			"epoch":      "",
			"target":     target.Hex()[2:],
			"algo":       "ethash",
			"extranonce": "",
		},
	}
	return cs.enc.Encode(&notification)
}

func (s *ProxyServer) broadcastNewJobs() {
	t := s.currentBlockTemplate()
	if t == nil || t.Header == nil || t.Target == nil || s.isSick() {
		return
	}

	s.sessionsMu.RLock()
	defer s.sessionsMu.RUnlock()

	count := len(s.sessions)
	log.Printf("Broadcasting new job to %v stratum miners", count)

	bcast := make(chan int, 1024)
	n := 0

	for m := range s.sessions {
		n++
		bcast <- n

		go func(cs *Session) {
			err := cs.pushNewJob(t.Header)
			<-bcast
			if err != nil {
				log.Printf("Job transmit error to %v@%v: %v", cs.login, cs.ip, err)
				s.removeSession(cs)
			}
		}(m)
	}
}

func (cs *Session) sendNewJob(header *types.Header, target *big.Int) {
	err := cs.pushNewJob(header)
	if err != nil {
		log.Printf("Job transmit error to %v@%v: %v", cs.login, cs.ip, err)
	}
}

func (s *ProxyServer) submitMinedHeader(cs *Session, header *types.Header) error {
	_, order, err := s.engine.CalcOrder(header)
	if err != nil {
		return fmt.Errorf("rejecting header: %v", err)
	}

	// Should be synchronous starting with the lowest levels.
	log.Printf("Received a %s block", strings.ToLower(common.OrderToString(order)))

	// Send mined header to the relevant go-quai nodes.
	// Should be synchronous starting with the lowest levels.
	for i := common.HierarchyDepth - 1; i >= order; i-- {
		err := s.rpc(i).SubmitMinedHeader(header)
		if err != nil {
			// Header was rejected. Refresh workers to try again.
			cs.sendNewJob(header, s.currentBlockTemplate().Target)
			return fmt.Errorf("rejected header: %v", err)
		}
	}

	return nil
}
