package proxy

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"time"
	"strconv"

	"github.com/J-A-M-P-S/go-etcstratum/util"
	"math/rand"
	"strings"
	_"github.com/davecgh/go-spew/spew"
)

func (s *ProxyServer) ListenNiceHashTCP() {
	timeout := util.MustParseDuration(s.config.Proxy.StratumNiceHash.Timeout)
	s.timeout = timeout

	addr, err := net.ResolveTCPAddr("tcp4", s.config.Proxy.StratumNiceHash.Listen)
	if err != nil {
		log.Fatalf("Error: %v", err)
	}
	server, err := net.ListenTCP("tcp4", addr)
	if err != nil {
		log.Fatalf("Error: %v", err)
	}
	defer server.Close()

	log.Printf("Stratum NiceHash listening on %s", s.config.Proxy.StratumNiceHash.Listen)
	var accept = make(chan int, s.config.Proxy.StratumNiceHash.MaxConn)
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
		cs := &Session{conn: conn, ip: ip, Extranonce: randomHex(4)}

		accept <- n
		go func(cs *Session) {
			err = s.handleNHTCPClient(cs)
			if err != nil {
				s.removeSession(cs)
				conn.Close()
			}
			<-accept
		}(cs)
	}
}

func (s *ProxyServer) handleNHTCPClient(cs *Session) error {
	cs.enc = json.NewEncoder(cs.conn)
	connbuff := bufio.NewReaderSize(cs.conn, MaxReqSize)
	s.setDeadline(cs.conn)

	log.Println("--- handleNHTCPClient, IP: ", cs.ip)

	for {
		data, isPrefix, err := connbuff.ReadLine()
		if isPrefix {
			log.Printf("Socket flood detected from %s", cs.ip)
			s.policy.BanClient(cs.ip)
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
			// DEBUG TRAFFIC
			//log.Printf(">>> %s\n", data)

			var req StratumReq
			err = json.Unmarshal(data, &req)
			if err != nil {
				s.policy.ApplyMalformedPolicy(cs.ip)
				log.Printf("Malformed stratum request from %s: %v", cs.ip, err)
				return err
			}
			s.setDeadline(cs.conn)
			err = cs.handleNHTCPMessage(s, &req)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func randomHex(strlen int) string {
	rand.Seed(time.Now().UTC().UnixNano())
	//const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	const chars = "0123456789abcdef"
	result := make([]byte, strlen)
	for i := 0; i < strlen; i++ {
		result[i] = chars[rand.Intn(len(chars))]
	}
	return string(result)
}

func (cs *Session) getNotificationResponse(s *ProxyServer, id *json.RawMessage) JSONRpcRespNH {
	result := make([]interface{}, 2)
	param1 := make([]string, 3)
	param1[0] = "mining.notify"
	param1[1] = randomHex(32)
	param1[2] = "EthereumStratum/1.0.0"
	result[0] = param1
	result[1] = cs.Extranonce

	resp := JSONRpcRespNH{
		Id:      id,
		//Version: "EthereumStratum/1.0.0",
		Result:  result,
		Error:   nil,
	}

	return resp
}

func (cs *Session) sendTCPNHError(id *json.RawMessage, message interface{}) error {
	cs.Mutex.Lock()
	defer cs.Mutex.Unlock()

	resp := JSONRpcRespNH{Id: id, Error: message}

	// DEBUG TRAFFIC
	//foo, _ := json.Marshal(resp)
	//log.Printf("<<< %s", string(foo))

	return cs.enc.Encode(&resp)
}

func (cs *Session) sendTCPNHResult(resp JSONRpcRespNH) error {
	cs.Mutex.Lock()
	defer cs.Mutex.Unlock()

	// DEBUG TRAFFIC
	//foo, _ := json.Marshal(resp)
	//log.Printf("<<< %s", string(foo))

	return cs.enc.Encode(&resp)
}

func (cs *Session) sendTCPNHReq(resp JSONRpcReqNH) error {
	cs.Mutex.Lock()
	defer cs.Mutex.Unlock()

	// DEBUG TRAFFIC
	//foo, _ := json.Marshal(resp)
	//log.Printf("<<< %s", string(foo))

	return cs.enc.Encode(&resp)
}

func (cs *Session) sendJob(s *ProxyServer, id *json.RawMessage) error {
	reply, errReply := s.handleGetWorkRPC(cs)
	if errReply != nil {
		return cs.sendTCPNHError(id, []string{
			string(errReply.Code),
			errReply.Message,
		})
	}

	cs.JobDetails = jobDetails{
		JobID:      randomHex(8),
		//JobID:      cs.JobDetails.JobID, // ??? test
		SeedHash:   reply[1],
		HeaderHash: reply[0],
	}

	// The NiceHash official .NET pool omits 0x...
	// TO DO: clean up once everything works
	if (cs.JobDetails.SeedHash[0:2] == "0x") {
		cs.JobDetails.SeedHash = cs.JobDetails.SeedHash[2:]
	}
	if (cs.JobDetails.HeaderHash[0:2] == "0x") {
		cs.JobDetails.HeaderHash = cs.JobDetails.HeaderHash[2:]
	}

	resp := JSONRpcReqNH{
		Method: "mining.notify",
		Params: []interface{}{
			cs.JobDetails.JobID,
			cs.JobDetails.SeedHash,
			cs.JobDetails.HeaderHash,
			true,
		},
	}

	return cs.sendTCPNHReq(resp)
}

func (cs *Session) handleNHTCPMessage(s *ProxyServer, req *StratumReq) error {
	// Handle RPC methods
	//log.Printf(">>> handleNHTCPMessage: %s %s\n", req.Method, req.Params)

	switch req.Method {
	case "mining.subscribe":
		var params []string
		err := json.Unmarshal(*req.Params, &params)
		if err != nil {
			log.Println("Malformed stratum request params from", cs.ip)
			return err
		}

		if params[1] != "EthereumStratum/1.0.0" {
			log.Println("Unsupported stratum version from ", cs.ip)
			return cs.sendTCPNHError(req.Id, "unsupported ethereum version")
		}

		resp := cs.getNotificationResponse(s, req.Id)
		return cs.sendTCPNHResult(resp)

	case "mining.authorize":
		var params []string
		err := json.Unmarshal(*req.Params, &params)
		if err != nil {
			return errors.New("invalid params")
		}
		splitData := strings.Split(params[0], ".")
		params[0] = splitData[0]
		reply, errReply := s.handleLoginRPC(cs, params, req.Worker)
		if errReply != nil {
			return cs.sendTCPNHError(req.Id, []string{
				string(errReply.Code),
				errReply.Message,
			})
		}

		resp := JSONRpcRespNH{Id: req.Id, Result: reply, Error: nil}
		if err := cs.sendTCPNHResult(resp); err != nil {
			return err
		}

		paramsDiff := []float64{
			s.config.Proxy.DifficultyNiceHash,
		}
		respReq := JSONRpcReqNH{Method: "mining.set_difficulty", Params: paramsDiff}
		if err := cs.sendTCPNHReq(respReq); err != nil {
			return err
		}

		// TEST
		//cs.JobDetails.JobID = randomHex(8)
		return cs.sendJob(s, req.Id)

	case "mining.submit":
		var params []string
		if err := json.Unmarshal(*req.Params, &params); err != nil {
			log.Println("mining.submit: json.Unmarshal fail")
			return err
		}

		// params[0] = Username
		// params[1] = Job ID
		// params[2] = Minernonce
		// Reference:
		// https://github.com/nicehash/nhethpool/blob/060817a9e646cd9f1092647b870ed625ee138ab4/nhethpool/EthereumInstance.cs#L369

		// WORKER NAME MANDATORY  0x1234.WORKERNAME
		// WILL CRASH IF OMITTED
		// FIX ME: pool shouldn't crash. reject submission instead.
		splitData := strings.Split(params[0], ".")
		id := splitData[1]

		if cs.JobDetails.JobID != params[1] {
			log.Printf("mining.submit: wrong JobID. Received: %s != %s", params[1], cs.JobDetails.JobID)
			return cs.sendTCPNHError(req.Id, []string{
				"21",
				"Stale share.",
			})
		}
		nonce := cs.Extranonce + params[2]

		params = []string{
			nonce,
			cs.JobDetails.SeedHash,
			cs.JobDetails.HeaderHash,
		}

		// test nicehash
		if (params[1][0:2] != "0x") {
			params[1] = "0x" + params[1]
		}
		if (params[2][0:2] != "0x") {
			params[2] = "0x" + params[2]
		}


		reply, errReply := s.handleTCPSubmitRPC(cs, id, params)
		if errReply != nil {
			log.Println("mining.submit: handleTCPSubmitRPC failed")
			return cs.sendTCPNHError(req.Id, []string{
				strconv.Itoa(errReply.Code),
				errReply.Message,
			})
		}
		resp := JSONRpcRespNH{
			Id:     req.Id,
			Result: reply,
			Error: false,
		}

		// TEST, ein notify zu viel
		//if err := cs.sendTCPNHResult(resp); err != nil {
		//	return err
		//}

		//return cs.sendJob(s, req.Id)
		return cs.sendTCPNHResult(resp) // ???

	default:
		errReply := s.handleUnknownRPC(cs, req.Method)
		return cs.sendTCPNHError(req.Id, []string{
			strconv.Itoa(errReply.Code),
			errReply.Message,
		})
	}
}


func (s *ProxyServer) broadcastNewJobsNH() {
	t := s.currentBlockTemplate()
	if t == nil || len(t.Header) == 0 || s.isSick() {
		return
	}

	s.sessionsMu.RLock()
	defer s.sessionsMu.RUnlock()

	count := len(s.sessions)
	log.Printf("Broadcasting new job to %v NiceHash miners", count)

	start := time.Now()
	bcast := make(chan int, 1024)
	n := 0

	for m, _ := range s.sessions {
		n++
		bcast <- n

		go func(cs *Session) {
			cs.JobDetails = jobDetails{
				JobID:      randomHex(8),
				SeedHash:   t.Seed,
				HeaderHash: t.Header,
			}

			// The NiceHash official .NET pool omits 0x...
			// TO DO: clean up once everything works
			if (cs.JobDetails.SeedHash[0:2] == "0x") {
				cs.JobDetails.SeedHash = cs.JobDetails.SeedHash[2:]
			}
			if (cs.JobDetails.HeaderHash[0:2] == "0x") {
				cs.JobDetails.HeaderHash = cs.JobDetails.HeaderHash[2:]
			}

			resp := JSONRpcReqNH{
				Method: "mining.notify",
				Params: []interface{}{
					cs.JobDetails.JobID,
					cs.JobDetails.SeedHash,
					cs.JobDetails.HeaderHash,
					true,
				},
			}

			err := cs.sendTCPNHReq(resp)
			<-bcast
			if err != nil {
				log.Printf("Job transmit error to %v@%v: %v", cs.login, cs.ip, err)
				s.removeSession(cs)
			} else {
				s.setDeadline(cs.conn)
			}
		}(m)
	}
	log.Printf("Jobs broadcast finished %s", time.Since(start))
}
