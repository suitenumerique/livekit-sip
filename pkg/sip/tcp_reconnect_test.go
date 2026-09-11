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
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/livekit/sipgo/sip"
	"github.com/livekit/sipgo/transaction"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// tcpProxy relays a TCP client to target, counts standalone CRLF keep-alives
// sent by the target and can drop every relayed connection on demand.
type tcpProxy struct {
	t      *testing.T
	ln     net.Listener
	target string

	mu    sync.Mutex
	conns []net.Conn

	accepted   atomic.Int32
	keepalives atomic.Int32
}

func newTCPProxy(t *testing.T, target string) *tcpProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	p := &tcpProxy{t: t, ln: ln, target: target}
	t.Cleanup(func() { ln.Close(); p.CloseConns() })
	go p.serve()
	return p
}

func (p *tcpProxy) Addr() string { return p.ln.Addr().String() }

func (p *tcpProxy) serve() {
	for {
		client, err := p.ln.Accept()
		if err != nil {
			return
		}
		upstream, err := net.Dial("tcp", p.target)
		if err != nil {
			client.Close()
			continue
		}
		p.accepted.Add(1)
		p.mu.Lock()
		p.conns = append(p.conns, client, upstream)
		p.mu.Unlock()
		go p.pipe(client, upstream, false)
		go p.pipe(upstream, client, true)
	}
}

// pipe copies src to dst; when fromTarget is set, reads made only of CRLF are
// counted as keep-alives (a SIP message never arrives as a bare CRLF chunk).
func (p *tcpProxy) pipe(src, dst net.Conn, fromTarget bool) {
	defer dst.Close()
	buf := make([]byte, 64*1024)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if fromTarget && len(bytes.Trim(buf[:n], "\r\n")) == 0 {
				p.keepalives.Add(int32(bytes.Count(buf[:n], []byte(crlfKeepalive))))
			}
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func (p *tcpProxy) CloseConns() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range p.conns {
		c.Close()
	}
	p.conns = nil
}

// pinDispatch keeps the call on the PIN prompt: it is accepted right away and
// stays established without ever joining a LiveKit room, like a caller idling
// on the PIN screen in production.
func pinDispatch(st *serviceTest) {
	st.Handler.(*TestHandler).DispatchCallFunc = func(ctx context.Context, info *CallInfo) CallDispatch {
		return CallDispatch{Result: DispatchRequestPin}
	}
}

func withTCPTransport(dest string) createCallTestOption {
	return func(req *sip.Request, _ *sip.Response) {
		if req == nil {
			return
		}
		req.SetTransport("TCP")
		if dest != "" {
			req.SetDestination(dest)
		}
	}
}

// answerReInvite serves the next server-initiated re-INVITE on sink with 200 OK.
func answerReInvite(t *testing.T, ctx context.Context, sink chan *sipUARequest, sdp []byte) <-chan *sip.Request {
	t.Helper()
	got := make(chan *sip.Request, 1)
	go func() {
		defer close(got)
		select {
		case msg := <-sink:
			if msg == nil {
				return
			}
			resp := sip.NewResponseFromRequest(msg.req, 200, "OK", sdp)
			resp.AppendHeader(&contentTypeHeaderSDP)
			_ = msg.tx.Respond(resp)
			got <- msg.req
		case <-ctx.Done():
		}
	}()
	return got
}

func waitEstablished(t *testing.T, ic *inboundCall) {
	t.Helper()
	require.Eventually(t, ic.cc.established, 2*time.Second, 10*time.Millisecond, "call should be answered and ACKed")
}

// dropInboundFlow closes the connection the INVITE came in on (what kamailio
// does after tcp_connection_lifetime) and waits until the server noticed.
func dropInboundFlow(t *testing.T, st *serviceTest, ic *inboundCall, proxy *tcpProxy) {
	t.Helper()
	src := ic.cc.invite.Source()
	proxy.CloseConns()
	require.Eventually(t, func() bool { return !st.Server.flowAlive("tcp", src) }, 2*time.Second, 10*time.Millisecond,
		"the closed inbound flow must leave the connection pool")
}

func waitKeepalive(t *testing.T, ic *inboundCall) *flowKeepalive {
	t.Helper()
	require.Eventually(t, func() bool { return ic.keepalive.Load() != nil }, 2*time.Second, 10*time.Millisecond,
		"keep-alive loop should be started for a TCP call")
	return ic.keepalive.Load()
}

func reconnectCount(t *testing.T, transport string) float64 {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	require.NoError(t, err)
	for _, f := range families {
		if f.GetName() != "livekit_sip_transport_reconnects_total" {
			continue
		}
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "transport" && l.GetValue() == transport {
					return m.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
}

func TestTCPFlowKeepaliveAndReconnect(t *testing.T) {
	st := NewServiceTest(t, nil)
	pinDispatch(st)
	st.Server.conf.SIPKeepaliveInterval = 50 * time.Millisecond
	proxy := newTCPProxy(t, st.Address())

	call, ic := st.CreateInboundCall(t, withTCPTransport(proxy.Addr()))
	t.Cleanup(func() { ic.Close() })
	waitEstablished(t, ic)
	ka := waitKeepalive(t, ic)
	require.Equal(t, int32(1), proxy.accepted.Load())

	// The INVITE flow is the proxy connection: keep-alives must show up there.
	require.Eventually(t, func() bool { return proxy.keepalives.Load() >= 2 }, 2*time.Second, 10*time.Millisecond,
		"expected double-CRLF keep-alives on the inbound TCP flow")
	src := ic.cc.invite.Source()
	require.True(t, st.Server.flowAlive("tcp", src), "inbound flow should be pooled")

	// Peer (kamailio) closes the flow: the keep-alive loop stops and the flow is gone.
	proxy.CloseConns()
	select {
	case <-ka.done:
	case <-time.After(2 * time.Second):
		require.Fail(t, "keep-alive loop should stop once the flow is closed")
	}
	require.Eventually(t, func() bool { return !st.Server.flowAlive("tcp", src) }, time.Second, 10*time.Millisecond)

	// A re-INVITE now goes straight to the routed destination (UA Contact) on a new connection.
	sink := st.TestUA.RegisterSink(call.localTag, "INVITE")
	defer st.TestUA.UnregisterSink(call.localTag, "INVITE")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	got := answerReInvite(t, ctx, sink, call.localSDP)

	before := reconnectCount(t, "tcp")
	resp, err := ic.cc.SendReInvite(ctx, call.remoteSDP)
	require.NoError(t, err)
	require.Equal(t, sip.StatusCode(200), resp.StatusCode)
	req := <-got
	require.NotNil(t, req)
	require.True(t, strings.EqualFold("tcp", req.Transport()), "re-INVITE should arrive over TCP, got %q", req.Transport())
	require.Equal(t, before, reconnectCount(t, "tcp"), "no reconnect needed: the dead flow was skipped up front")
	require.Equal(t, int32(1), proxy.accepted.Load(), "the routed destination must not go through the closed flow")
}

func TestTCPTransactionRetryOnTransportError(t *testing.T) {
	st := NewServiceTest(t, nil)
	pinDispatch(st)
	proxy := newTCPProxy(t, st.Address())
	call, ic := st.CreateInboundCall(t, withTCPTransport(proxy.Addr()))
	t.Cleanup(func() { ic.Close() })
	waitEstablished(t, ic)
	dropInboundFlow(t, st, ic, proxy)

	// First re-INVITE: dials the UA and leaves a pooled outbound connection behind.
	sink := st.TestUA.RegisterSink(call.localTag, "INVITE")
	defer st.TestUA.UnregisterSink(call.localTag, "INVITE")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	got := answerReInvite(t, ctx, sink, call.localSDP)
	resp, err := ic.cc.SendReInvite(ctx, call.remoteSDP)
	require.NoError(t, err)
	require.Equal(t, sip.StatusCode(200), resp.StatusCode)
	first := <-got
	require.NotNil(t, first)
	uaAddr := st.TestUA.localAddr.String()
	require.True(t, st.Server.flowAlive("tcp", uaAddr), "outbound connection to the UA should be pooled")

	// Second re-INVITE: the pooled connection fails at write time (stale peer).
	orig := st.Server.txRequest
	var failures atomic.Int32
	st.Server.txRequest = func(req *sip.Request) (sip.ClientTransaction, error) {
		if failures.CompareAndSwap(0, 1) {
			return nil, fmt.Errorf("conn %s write err=%w. %w", req.Destination(), io.EOF, transaction.ErrTransport)
		}
		return orig(req)
	}
	t.Cleanup(func() { st.Server.txRequest = orig })

	before := reconnectCount(t, "tcp")
	got = answerReInvite(t, ctx, sink, call.localSDP)
	resp, err = ic.cc.SendReInvite(ctx, call.remoteSDP)
	require.NoError(t, err, "re-INVITE must survive one stale-connection write failure")
	require.Equal(t, sip.StatusCode(200), resp.StatusCode)
	second := <-got
	require.NotNil(t, second)
	require.NotEqual(t, first.Via().Params.GetOr("branch", ""), second.Via().Params.GetOr("branch", ""))
	require.Equal(t, before+1, reconnectCount(t, "tcp"), "one reconnect must be counted")
	require.Eventually(t, func() bool { return st.Server.flowAlive("tcp", uaAddr) }, time.Second, 10*time.Millisecond,
		"the retry must have dialed a fresh connection")
}

func TestEvictConnection(t *testing.T) {
	st := NewServiceTest(t, nil)
	pinDispatch(st)
	proxy := newTCPProxy(t, st.Address())
	call, ic := st.CreateInboundCall(t, withTCPTransport(proxy.Addr()))
	t.Cleanup(func() { ic.Close() })
	waitEstablished(t, ic)
	dropInboundFlow(t, st, ic, proxy)

	sink := st.TestUA.RegisterSink(call.localTag, "INVITE")
	defer st.TestUA.UnregisterSink(call.localTag, "INVITE")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	got := answerReInvite(t, ctx, sink, call.localSDP)
	_, err := ic.cc.SendReInvite(ctx, call.remoteSDP)
	require.NoError(t, err)
	require.NotNil(t, <-got)
	uaAddr := st.TestUA.localAddr.String()
	require.True(t, st.Server.flowAlive("tcp", uaAddr))

	require.True(t, st.Server.evictConnection("tcp", uaAddr))
	require.False(t, st.Server.flowAlive("tcp", uaAddr), "evicted connection must leave the pool")
	require.False(t, st.Server.evictConnection("tcp", uaAddr), "nothing left to evict")
	require.False(t, st.Server.evictConnection("udp", uaAddr), "datagram transports are never evicted")

	// The next request dials again transparently.
	got = answerReInvite(t, ctx, sink, call.localSDP)
	resp, err := ic.cc.SendReInvite(ctx, call.remoteSDP)
	require.NoError(t, err)
	require.Equal(t, sip.StatusCode(200), resp.StatusCode)
	require.NotNil(t, <-got)
	require.True(t, st.Server.flowAlive("tcp", uaAddr))
}

func TestFlowKeepaliveLifecycle(t *testing.T) {
	t.Run("tcp stops on close", func(t *testing.T) {
		st := NewServiceTest(t, nil)
		pinDispatch(st)
		st.Server.conf.SIPKeepaliveInterval = 20 * time.Millisecond
		call, ic := st.CreateInboundCall(t, withTCPTransport(""))
		ka := waitKeepalive(t, ic)

		byeSink := st.TestUA.RegisterSink(call.localTag, "BYE")
		defer st.TestUA.UnregisterSink(call.localTag, "BYE")
		closed := make(chan error, 1)
		go func() { closed <- ic.Close() }()
		select {
		case msg := <-byeSink:
			require.NotNil(t, msg)
			_ = msg.tx.Respond(sip.NewResponseFromRequest(msg.req, 200, "OK", nil))
		case <-time.After(5 * time.Second):
			require.Fail(t, "timeout waiting for BYE")
		}
		require.NoError(t, <-closed)
		select {
		case <-ka.done:
		case <-time.After(2 * time.Second):
			require.Fail(t, "keep-alive loop should exit when the call closes")
		}
	})
	t.Run("udp has none", func(t *testing.T) {
		st := NewServiceTest(t, nil)
		pinDispatch(st)
		st.Server.conf.SIPKeepaliveInterval = 20 * time.Millisecond
		_, ic := st.CreateInboundCall(t)
		t.Cleanup(func() { ic.Close() })
		time.Sleep(200 * time.Millisecond)
		require.Nil(t, ic.keepalive.Load(), "no keep-alive loop for UDP calls")
	})
	t.Run("disabled", func(t *testing.T) {
		st := NewServiceTest(t, nil)
		pinDispatch(st)
		st.Server.conf.SIPKeepaliveInterval = -1
		_, ic := st.CreateInboundCall(t, withTCPTransport(""))
		t.Cleanup(func() { ic.Close() })
		time.Sleep(200 * time.Millisecond)
		require.Nil(t, ic.keepalive.Load(), "negative interval disables the keep-alive")
	})
}
