package proxy

import (
	"bufio"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"

	"github.com/dominant-strategies/go-quai-stratum/util"
	"github.com/dominant-strategies/go-quai/common"
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
			log.Printf("Error accepting connection: %v", err)
			continue
		}
		conn.SetKeepAlive(true)

		ip, _, _ := net.SplitHostPort(conn.RemoteAddr().String())

		if s.policy.IsBanned(ip) || !s.policy.ApplyLimitPolicy(ip) {
			conn.Close()
			continue
		}
		n += 1
		cs := &Session{
			conn:       conn,
			ip:         ip,
			Extranonce: fmt.Sprintf("%x", s.rng.Intn(0xffff)),
		}

		accept <- n
		go func(cs *Session) {
			err = s.handleTCPClient(cs)
			if err != nil {
				log.Printf("Error handling client: %v", err)
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
				"maxerrors": 999,
				"node":      "go-quai/development",
			},
		}
		return cs.sendMessage(&response)

	case "mining.bye":
		s.removeSession(cs)
		return nil

	case "mining.subscribe":
		response := Response{
			ID:     req.Id,
			Result: "s-12345",
		}
		return cs.sendMessage(&response)

	case "mining.authorize":
		response := Response{
			ID:     req.Id,
			Result: "s-12345",
		}
		err := cs.sendMessage(&response)
		if err != nil {
			log.Printf("Error encoding JSON: %v", err)
			return err
		}
		return s.handleLoginRPC(cs, *req)

	case "mining.submit":
		var errorResponse Response

		jobId, err := strconv.ParseUint(req.Params.([]interface{})[0].(string), 16, 0)
		if err != nil {
			log.Printf("Error decoding jobID: %v", err)
			errorResponse = Response{
				ID: req.Id,
				Error: map[string]interface{}{
					"code":    500,
					"message": "Bad jobID",
				},
			}
			return cs.sendMessage(&errorResponse)
		}

		nonceStr := cs.Extranonce + req.Params.([]interface{})[1].(string)
		nonce, err := hex.DecodeString(nonceStr)
		if err != nil {
			log.Printf("Error decoding nonce: %v", err)
			errorResponse = Response{
				ID: req.Id,
				Error: map[string]interface{}{
					"code":    405,
					"message": "Invalid nonce parameter",
				},
			}
			return cs.sendMessage(&errorResponse)
		}

		header, err := s.verifyMinedHeader(uint(jobId), nonce)
		if err != nil {
			log.Printf("Unable to verify header: %v", err)
			errorResponse = Response{
				ID: req.Id,
				Error: map[string]interface{}{
					"code":    406,
					"message": "Bad nonce",
				},
			}
			return cs.sendMessage(&errorResponse)
		}

		err = s.submitMinedHeader(cs, header)
		if err != nil {
			log.Printf("Error submitting header: %v", err)
			errorResponse = Response{
				ID: req.Id,
				Error: map[string]interface{}{
					"code":    406,
					"message": "Bad nonce",
				},
			}
			return cs.sendMessage(&errorResponse)
		}
		successResponse := Response{
			ID: req.Id,
		}
		return cs.sendMessage(&successResponse)

	default:
		return nil
	}
}

func (cs *Session) sendTCPResult(id uint, result interface{}) error {
	message := Response{
		ID:     id,
		Result: result,
		Error:  nil,
	}

	return cs.sendMessage(&message)
}

func (cs *Session) sendTCPError(err error) {
	message := Response{
		ID:     0,
		Result: nil,
		Error:  err,
	}

	cs.sendMessage(&message)
}

// func (cs *Session) pushNewJob(header *types.Header, target *big.Int) error {
func (cs *Session) pushNewJob(template *BlockTemplate) error {
	// Update target to worker.
	cs.setMining(common.BytesToHash(template.Target.Bytes()))

	notification := Notification{
		Method: "mining.notify",
		Params: []string{
			fmt.Sprintf("%x", template.JobID),
			fmt.Sprintf("%x", template.Header.Number(common.ZONE_CTX)),
			fmt.Sprintf("%x", template.Header.SealHash()),
			"0",
		},
	}
	return cs.sendMessage(&notification)
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
	notification := Notification{
		Method: "mining.set",
		Params: map[string]interface{}{
			"epoch":      "",
			"target":     target.Hex()[2:],
			"algo":       "ethash",
			"extranonce": cs.Extranonce,
		},
	}
	return cs.sendMessage(&notification)
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
			err := cs.pushNewJob(t)
			<-bcast
			if err != nil {
				log.Printf("Job transmit error to %v@%v: %v", cs.login, cs.ip, err)
				s.removeSession(cs)
			}
		}(m)
	}
}

func (cs *Session) sendMessage(v any) error {
	cs.Lock()
	defer cs.Unlock()

	return cs.enc.Encode(v)
}
