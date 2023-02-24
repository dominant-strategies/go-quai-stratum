package proxy

import (
	"bufio"
	"encoding/json"

	"fmt"
	"io"
	"log"
	"net"
	"time"

	"github.com/INFURA/go-ethlibs/jsonrpc"
	"github.com/dominant-strategies/go-quai-stratum/util"

	"github.com/dominant-strategies/go-quai/common"
	"github.com/dominant-strategies/go-quai/core/types"
	"github.com/dominant-strategies/go-quai/quaiclient"
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

func (s *ProxyServer) handleTCPClient(cs *Session) error {
	cs.enc = json.NewEncoder(cs.conn)
	connbuff := bufio.NewReaderSize(cs.conn, c_Max_Req_Size)
	for {
		data, isPrefix, err := connbuff.ReadLine()
		if isPrefix {
			log.Printf("Socket flood detected from %s", cs.ip)
			cs.sendTCPError(jsonrpc.LimitExceeded(fmt.Sprintf("Message exceeds proxy's buffer size of %v", c_Max_Req_Size)))
			s.policy.BanClient(cs.ip)
			s.removeSession(cs)
			return err
		} else if err == io.EOF {
			log.Printf("Client %s disconnected", cs.ip)
			s.removeSession(cs)
			break
		} else if err != nil {
			log.Printf("Error reading from socket: %v", err)
			return err
		}

		if len(data) > 1 {
			var req jsonrpc.Request
			err = req.UnmarshalJSON(data)
			if err != nil {
				s.policy.ApplyMalformedPolicy(cs.ip)
				log.Printf("Malformed stratum request from %s: %v", cs.ip, err)
				return err
			}
			err = cs.handleTCPMessage(s, &req)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (cs *Session) handleTCPMessage(s *ProxyServer, req *jsonrpc.Request) error {
	// Handle RPC methods
	switch req.Method {
	case "quai_submitLogin":
		_, errReply := s.handleLoginRPC(cs, req.Params)
		if errReply != nil {
			return cs.sendTCPError(jsonrpc.MethodNotFound(req))
		}
		return nil
	case "quai_getPendingHeader":
		reply, errReply := s.handleGetWorkRPC(cs)
		if errReply != nil {
			return cs.sendTCPError(jsonrpc.NewError(-1, errReply.Message))
		}
		header_rep := quaiclient.RPCMarshalHeader(reply)
		cs.sendTCPResult(req.ID.String(), header_rep)
		return nil
	case "quai_receiveMinedHeader":
		var received_header *types.Header
		err := json.Unmarshal(req.Params[0], &received_header)
		if err != nil {
			log.Printf("Unable to decode header from %v. Err: %v", cs.ip, err)
			return err
		}
		order, err := GetDifficultyOrder(received_header)
		if err != nil {
			log.Fatalf("Unable to get order of mined headr: %v", err)
			return err
		}

		// Send mined header to the relevant go-quai nodes.
		// Should be synchronous starting with the lowest levels.
		for i := common.HierarchyDepth - 1; i >= order; i-- {
			s.rpc(i).SubmitMinedHeader(received_header)
		}

		return nil
	default:
		return cs.sendTCPError(jsonrpc.MethodNotFound(req))
	}
}

func (cs *Session) sendTCPResult(id string, result interface{}) error {
	cs.Lock()
	defer cs.Unlock()

	message := jsonrpc.Response{
		ID:     jsonrpc.StringID(id),
		Result: result,
		Error:  nil,
	}

	return cs.enc.Encode(&message)
}

func (cs *Session) pushNewJob(result *types.Header) error {
	cs.Lock()
	defer cs.Unlock()
	// FIXME: Temporarily add ID for Claymore compliance <- (from J-A-M-P-S fork)
	message := jsonrpc.NewResponse()
	message.Result = quaiclient.RPCMarshalHeader(result)

	return cs.enc.Encode(message)
}

func (cs *Session) sendTCPError(err *jsonrpc.Error) error {
	cs.Lock()
	defer cs.Unlock()

	return cs.enc.Encode(err)
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

func (s *ProxyServer) broadcastNewJobs() {
	t := s.currentBlockTemplate()
	if t == nil || t.Header == nil || s.isSick() {
		return
	}
	reply := t.Header

	s.sessionsMu.RLock()
	defer s.sessionsMu.RUnlock()

	count := len(s.sessions)
	log.Printf("Broadcasting new job to %v stratum miners", count)

	start := time.Now()
	bcast := make(chan int, 1024)
	n := 0

	for m := range s.sessions {
		n++
		bcast <- n

		go func(cs *Session) {
			err := cs.pushNewJob(reply)
			<-bcast
			if err != nil {
				log.Printf("Job transmit error to %v@%v: %v", cs.login, cs.ip, err)
				s.removeSession(cs)
			}
		}(m)
	}
	log.Printf("Jobs broadcast finished %s", time.Since(start))
}
