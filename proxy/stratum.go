package proxy

import (
	"bufio"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strconv"

	"github.com/dominant-strategies/go-quai-stratum/util"
	"github.com/dominant-strategies/go-quai/common"
	"github.com/dominant-strategies/go-quai/consensus/progpow"
	"github.com/dominant-strategies/go-quai/core/types"
	"github.com/dominant-strategies/go-quai/log"
)

const (
	c_Max_Req_Size = 4096
)

func (s *ProxyServer) ListenTCP() {
	timeout := util.MustParseDuration(s.config.Proxy.Stratum.Timeout)
	s.timeout = timeout

	addr, err := net.ResolveTCPAddr("tcp4", s.config.Proxy.Stratum.Listen)
	if err != nil {
		log.Global.WithFields(log.Fields{
			"addr": addr,
			"err":  err,
		}).Fatalf("Unable to resolve TCP address")
	}
	server, err := net.ListenTCP("tcp4", addr)
	if err != nil {
		log.Global.WithFields(log.Fields{
			"addr": addr,
			"err":  err,
		}).Fatalf("Unable to bind to specified TCP address")
	}
	defer server.Close()

	log.Global.WithField("address", s.config.Proxy.Stratum.Listen).Info("Stratum listening address")
	var accept = make(chan int, s.config.Proxy.Stratum.MaxConn)

	n := 0
	for {
		conn, err := server.AcceptTCP()
		if err != nil {
			log.Global.WithField("err", err).Warn("Error accepting connection")
			continue
		}
		conn.SetKeepAlive(true)

		ip, port, _ := net.SplitHostPort(conn.RemoteAddr().String())

		if s.policy.IsBanned(ip) || !s.policy.ApplyLimitPolicy(ip) {
			conn.Close()
			continue
		}
		n += 1
		cs := &Session{
			conn:       conn,
			ip:         ip,
			port:       port,
			sealMining: s.config.Proxy.SealMining,
			Extranonce: fmt.Sprintf("%04x", s.rng.Intn(0xffff)),
		}

		accept <- n
		go func(cs *Session) {
			err = s.handleTCPClient(cs)
			if err != nil {
				log.Global.WithField("err", err).Warn("Error handling client")
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
			log.Global.WithFields(log.Fields{
				"ip":   cs.ip,
				"port": cs.port,
				"err":  err,
			}).Warn("Socket flood detected")
			s.policy.BanClient(cs.ip)
			cs.sendTCPError(err)
			s.removeSession(cs)
			return err
		} else if err == io.EOF {
			log.Global.WithFields(log.Fields{
				"ip":   cs.ip,
				"port": cs.port,
				"err":  err,
			}).Warn("Client disconnected")
			cs.sendTCPError(err)
			s.removeSession(cs)
			break
		} else if err != nil {
			log.Global.WithField("err", err).Warn("Error reading from socket")
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
			log.Global.WithFields(log.Fields{
				"err":    err,
				"client": cs.ip,
				"port":   cs.port,
			}).Warn("Error encoding JSON")
			return err
		}
		return s.handleLoginRPC(cs, *req)

	case "mining.submit":
		var errorResponse Response

		jobId, err := strconv.ParseUint(req.Params.([]interface{})[0].(string), 16, 0)
		if err != nil {
			log.Global.WithFields(log.Fields{
				"err":    err,
				"client": cs.ip,
				"port":   cs.port,
			}).Warn("Error decoding jobID")
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
			log.Global.WithFields(log.Fields{
				"err":    err,
				"client": cs.ip,
				"port":   cs.port,
			}).Warn("Error decoding nonce")
			errorResponse = Response{
				ID: req.Id,
				Error: map[string]interface{}{
					"code":    405,
					"message": "Invalid nonce parameter",
				},
			}
			return cs.sendMessage(&errorResponse)
		}

		// If in seal mining mode, the nonce is simply returned to the host.
		if s.config.Proxy.SealMining {
			err := s.receiveNonce(uint(jobId), types.BlockNonce(nonce))
			if err != nil {
				log.Global.WithFields(log.Fields{
					"err":    err,
					"client": cs.ip,
					"port":   cs.port,
				}).Warn("Unable to verify header")
				errorResponse = Response{
					ID: req.Id,
					Error: map[string]interface{}{
						"code":    406,
						"message": "Bad nonce",
					},
				}
				cs.setMining(s.currentBlockTemplate())
				return cs.sendMessage(&errorResponse)
			}
			log.Global.Info("Miner submitted a workShare")
		} else {
			header, err := s.verifyMinedHeader(uint(jobId), nonce)
			if err != nil {
				log.Global.WithFields(log.Fields{
					"err":    err,
					"client": cs.ip,
					"port":   cs.port,
				}).Warn("Unable to verify header")
				errorResponse = Response{
					ID: req.Id,
					Error: map[string]interface{}{
						"code":    406,
						"message": "Bad nonce",
					},
				}
				cs.setMining(s.currentBlockTemplate())
				return cs.sendMessage(&errorResponse)
			}

			err = s.submitMinedHeader(cs, header)
			if err != nil {
				log.Global.WithField("workShareHash", header.Hash()).Info("Miner submitted a workShare")
			} else {
				log.Global.WithFields(log.Fields{
					"location":  s.config.Upstream.Name,
					"number":    header.NumberArray(),
					"blockhash": header.Hash(),
				}).Info("Miner submitted a block")
			}
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
	cs.setMining(template)

	var notification Notification = Notification{
		Method: "mining.notify",
		Params: []string{
			fmt.Sprintf("%x", template.JobID),
		},
	}
	if cs.sealMining {
		// The SealHash is provided directly by the template.
		notification.Params = append(notification.Params.([]string),
			fmt.Sprintf("%x", template.PrimeTerminusNumber), // Mining clients should not use this number to determine the epoch according to EthStratumV2.
			fmt.Sprintf("%x", template.CustomSeal),
		)
	} else {
		// The SealHash is generated from the work object.
		notification.Params = append(notification.Params.([]string),
			fmt.Sprintf("%x", template.WorkObject.PrimeTerminusNumber().Uint64()),
			fmt.Sprintf("%x", template.WorkObject.SealHash()),
		)
	}
	notification.Params = append(notification.Params.([]string), "0")
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

// func (cs *Session) setMining(target common.Hash) error {
func (cs *Session) setMining(template *BlockTemplate) error {
	var notification Notification
	if !cs.sealMining {
		notification = Notification{
			Method: "mining.set",
			Params: map[string]any{
				"epoch":      fmt.Sprintf("%x", int(template.WorkObject.PrimeTerminusNumber().Uint64()/progpow.C_epochLength)),
				"target":     common.BytesToHash(template.Target.Bytes()).Hex()[2:],
				"algo":       "progpow",
				"extranonce": cs.Extranonce,
			},
		}
	} else {
		// We are SealMining.
		notification = Notification{
			Method: "mining.set",
			Params: map[string]any{
				"epoch":      fmt.Sprintf("%x", int(template.PrimeTerminusNumber.Uint64()/progpow.C_epochLength)),
				"target":     common.BytesToHash(template.Target.Bytes()).Hex()[2:],
				"algo":       "progpow",
				"extranonce": cs.Extranonce,
			},
		}
	}
	return cs.sendMessage(&notification)
}

func (s *ProxyServer) broadcastNewJobs() {
	t := s.currentBlockTemplate()
	if !s.config.Proxy.SealMining && (t == nil || t.WorkObject == nil || t.Target == nil || s.isSick()) {
		return
	}

	s.sessionsMu.RLock()
	defer s.sessionsMu.RUnlock()

	count := len(s.sessions)
	if !s.config.Proxy.SealMining {
		log.Global.Printf("Broadcasting block %d to %d stratum miners", t.WorkObject.NumberU64(common.ZONE_CTX), count)
	} else {
		log.Global.Printf("Broadcasting to %d stratum miners", count)
	}

	bcast := make(chan int, 1024)
	n := 0

	for m := range s.sessions {
		n++
		bcast <- n

		go func(cs *Session) {
			err := cs.pushNewJob(t)
			<-bcast
			if err != nil {
				log.Global.WithFields(log.Fields{
					"login": cs.login,
					"ip":    cs.ip,
					"port":  cs.port,
					"err":   err,
				}).Warn("Job transmit error")
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
