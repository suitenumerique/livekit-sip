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
	"context"
	"errors"
	"net"
	"syscall"
	"testing"

	"github.com/go-gst/go-gst/gst"
	"golang.org/x/sys/unix"
)

func portFree(port int) bool {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: port})
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func TestReadyToNullReleasesUDPPorts(t *testing.T) {
	f := newFixture(t, []*gst.Caps{pcmuCaps(), h264Caps()})
	defer f.close()

	answer := f.emitOffer(t, makeSDP("192.168.1.1",
		"m=audio 5000 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000",
		"m=video 5002 RTP/AVP 120\r\na=rtpmap:120 H264/90000\r\na=content:main",
	))
	f.emitAck(t)

	msg := parseAnswer(t, answer)
	var ports []int
	for i := range msg.MediasLen() {
		port := int(msg.Media(i).GetPort())
		if port == 0 {
			t.Fatalf("media %d disabled in the answer", i)
		}
		ports = append(ports, port, port+1)
	}
	for _, port := range ports {
		if portFree(port) {
			t.Fatalf("port %d is free while the call is up", port)
		}
	}

	if err := f.pipeline.SetState(gst.StateNull); err != nil {
		t.Fatalf("failed to set pipeline to NULL: %v", err)
	}
	for _, port := range ports {
		if !portFree(port) {
			t.Errorf("port %d is still held after the SIP bin went to NULL", port)
		}
	}
}

func TestNewUDPConnPairSkipsAPortHeldWithReusePort(t *testing.T) {
	lc := net.ListenConfig{
		Control: func(_, _ string, c syscall.RawConn) error {
			var sockErr error
			if err := c.Control(func(fd uintptr) {
				sockErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEPORT, 1)
			}); err != nil {
				return err
			}
			return sockErr
		},
	}
	var held net.PacketConn
	port := 0
	for range 100 {
		conn, err := lc.ListenPacket(context.Background(), "udp4", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("failed to bind a test port: %v", err)
		}
		if p := conn.LocalAddr().(*net.UDPAddr).Port; p%2 == 0 && p < 0xFFFF {
			held, port = conn, p
			break
		}
		conn.Close()
	}
	if held == nil {
		t.Fatal("no even test port found")
	}
	defer held.Close()

	rtp, rtcp, err := NewUDPConnPair(uint16(port), uint16(port+1), net.ParseIP("127.0.0.1"))
	if err == nil {
		rtp.Close()
		rtcp.Close()
		t.Fatalf("port %d was handed out while another socket holds it", port)
	}
	if !errors.Is(err, ErrListenFailed) {
		t.Fatalf("expected ErrListenFailed, got %v", err)
	}
}
