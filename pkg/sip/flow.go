// Copyright 2023 LiveKit, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package sip

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/livekit/protocol/logger"
	"github.com/livekit/sipgo/sip"
	"github.com/livekit/sipgo/transaction"
	"github.com/livekit/sipgo/transport"
)

const (
	// crlfKeepalive is the RFC 5626 §4.4.1 double-CRLF ping sent on a stream flow.
	crlfKeepalive = "\r\n\r\n"
	// evictWait bounds how long evictConnection waits for the transport reader to
	// drop a closed connection from its pool.
	evictWait = 200 * time.Millisecond
)

// flowAlive reports whether the transport layer still holds a stream (TCP/TLS)
// connection to addr. It is always false for datagram transports.
func (s *Server) flowAlive(network, addr string) bool {
	if addr == "" || !transport.IsReliable(network) {
		return false
	}
	conn, err := s.sipSrv.TransportLayer().GetConnection(network, addr)
	if err != nil || conn == nil {
		return false
	}
	conn.TryClose()
	return true
}

// evictConnection closes the pooled stream connection to addr so the next
// request dials a fresh one. Only the socket is closed: the transport reader
// observes the close and removes the pool entry with its usual bookkeeping.
// Returns true if a connection was found and closed.
func (s *Server) evictConnection(network, addr string) bool {
	if addr == "" || !transport.IsReliable(network) {
		return false
	}
	tpl := s.sipSrv.TransportLayer()
	conn, err := tpl.GetConnection(network, addr)
	if err != nil || conn == nil {
		return false
	}
	conn.TryClose()
	if tc, ok := conn.(*transport.TCPConnection); ok {
		_ = tc.Conn.Close()
	} else {
		_ = conn.Close()
	}
	deadline := time.Now().Add(evictWait)
	for time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
		cur, err := tpl.GetConnection(network, addr)
		if err != nil || cur == nil {
			return true
		}
		cur.TryClose()
		if cur != conn {
			return true
		}
	}
	return true
}

// recoverTransport prepares a retry of req after a stream transport failure:
// the connection behind req's destination is evicted and the Via branch is
// renewed so the retry is a new transaction. Returns false when req does not
// use a stream transport, in which case the caller must not retry.
func (s *Server) recoverTransport(req *sip.Request, cause error, log logger.Logger) bool {
	network := req.Transport()
	if !transport.IsReliable(network) {
		return false
	}
	evicted := s.evictConnection(network, req.Destination())
	if via := req.Via(); via != nil {
		via.Params.Add("branch", sip.GenerateBranchN(16))
	}
	tr := strings.ToLower(network)
	s.mon.TransportReconnect(tr)
	log.Infow("sip transport reconnect",
		"transport", tr, "dest", req.Destination(), "evicted", evicted, "error", cause.Error())
	return true
}

// transactionRequest starts a client transaction for req, retrying once on a
// fresh connection when the first attempt fails at the stream transport level.
func (s *Server) transactionRequest(req *sip.Request, log logger.Logger) (sip.ClientTransaction, error) {
	tx, err := s.txRequest(req)
	if err == nil || !errors.Is(err, transaction.ErrTransport) {
		return tx, err
	}
	if !s.recoverTransport(req, err, log) {
		return nil, err
	}
	return s.txRequest(req)
}

// writeRequest sends req outside of a transaction (ACK), retrying once on a
// fresh connection when the first write fails on a stream transport.
func (s *Server) writeRequest(req *sip.Request, log logger.Logger) error {
	err := s.sipSrv.TransportLayer().WriteMsg(req)
	if err == nil {
		return nil
	}
	if !s.recoverTransport(req, err, log) {
		return err
	}
	return s.sipSrv.TransportLayer().WriteMsg(req)
}

// startFlowKeepalive sends double-CRLF keep-alives on the stream flow addr every
// interval until ctx is done or the flow disappears. The returned channel is
// closed when the keep-alive loop exits; it is nil when no loop was started.
func (s *Server) startFlowKeepalive(ctx context.Context, network, addr string, interval time.Duration, log logger.Logger) <-chan struct{} {
	if interval <= 0 || addr == "" || !transport.IsReliable(network) {
		return nil
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.runFlowKeepalive(ctx, network, addr, interval, log)
	}()
	return done
}

func (s *Server) runFlowKeepalive(ctx context.Context, network, addr string, interval time.Duration, log logger.Logger) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if err := s.writeKeepalive(network, addr); err != nil {
			log.Infow("sip flow keepalive stopped", "transport", strings.ToLower(network), "flow", addr, "error", err.Error())
			return
		}
	}
}

func (s *Server) writeKeepalive(network, addr string) error {
	conn, err := s.sipSrv.TransportLayer().GetConnection(network, addr)
	if err != nil {
		return err
	}
	defer conn.TryClose()
	w, ok := conn.(io.Writer)
	if !ok {
		return fmt.Errorf("connection to %s does not support raw writes", addr)
	}
	_, err = w.Write([]byte(crlfKeepalive))
	return err
}
