// Copyright 2026 LiveKit, Inc.
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

package sipbin

import (
	"errors"
	"net"
	"testing"
)

var loopback = net.ParseIP("127.0.0.1")

func freeEvenPort(t *testing.T, span int) int {
	t.Helper()
	for range 200 {
		conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: loopback})
		if err != nil {
			t.Fatalf("failed to bind a test port: %v", err)
		}
		port := conn.LocalAddr().(*net.UDPAddr).Port
		conn.Close()
		if port%2 != 0 || port+span > 0xFFFF {
			continue
		}
		free := true
		for p := port; p < port+span; p++ {
			if !portFree(p) {
				free = false
				break
			}
		}
		if free {
			return port
		}
	}
	t.Fatal("no free even port range found")
	return 0
}

func TestNewUDPConnPairNeverSharesAPort(t *testing.T) {
	port := freeEvenPort(t, 2)

	rtp, rtcp, err := NewUDPConnPair(uint16(port), uint16(port+1), loopback)
	if err != nil {
		t.Fatalf("first pair: %v", err)
	}
	if got := rtp.LocalAddr().(*net.UDPAddr).Port; got != port {
		t.Fatalf("RTP port %d, expected %d", got, port)
	}
	if got := rtcp.LocalAddr().(*net.UDPAddr).Port; got != port+1 {
		t.Fatalf("RTCP port %d, expected %d", got, port+1)
	}

	if r2, c2, err := NewUDPConnPair(uint16(port), uint16(port+1), loopback); err == nil {
		r2.Close()
		c2.Close()
		t.Fatalf("pair %d/%d handed out twice", port, port+1)
	} else if !errors.Is(err, ErrListenFailed) {
		t.Fatalf("expected ErrListenFailed, got %v", err)
	}

	rtp.Close()
	rtcp.Close()
	r3, c3, err := NewUDPConnPair(uint16(port), uint16(port+1), loopback)
	if err != nil {
		t.Fatalf("pair not reusable after release: %v", err)
	}
	r3.Close()
	c3.Close()
}

func TestNewUDPConnPairKeepsRTCPInRange(t *testing.T) {
	port := freeEvenPort(t, 4)
	heldRTP, heldRTCP, err := NewUDPConnPair(uint16(port), uint16(port+1), loopback)
	if err != nil {
		t.Fatalf("first pair: %v", err)
	}
	defer heldRTP.Close()
	defer heldRTCP.Close()

	rtp, rtcp, err := NewUDPConnPair(uint16(port), uint16(port+2), loopback)
	if err == nil {
		rtpPort := rtp.LocalAddr().(*net.UDPAddr).Port
		rtcpPort := rtcp.LocalAddr().(*net.UDPAddr).Port
		rtp.Close()
		rtcp.Close()
		t.Fatalf("pair %d/%d handed out from the range %d-%d", rtpPort, rtcpPort, port, port+2)
	}
	if !errors.Is(err, ErrListenFailed) {
		t.Fatalf("expected ErrListenFailed, got %v", err)
	}
}

func TestNewUDPConnPairFailsWhenTheRangeIsFull(t *testing.T) {
	port := freeEvenPort(t, 6)
	hi := port + 5

	var held []*net.UDPConn
	defer func() {
		for _, c := range held {
			c.Close()
		}
	}()
	for range 3 {
		rtp, rtcp, err := NewUDPConnPair(uint16(port), uint16(hi), loopback)
		if err != nil {
			t.Fatalf("pair: %v", err)
		}
		held = append(held, rtp, rtcp)
	}

	if rtp, rtcp, err := NewUDPConnPair(uint16(port), uint16(hi), loopback); err == nil {
		rtp.Close()
		rtcp.Close()
		t.Fatal("a pair was handed out from a full range")
	} else if !errors.Is(err, ErrListenFailed) {
		t.Fatalf("expected ErrListenFailed, got %v", err)
	}
}
