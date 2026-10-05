// Copyright 2026 LiveKit, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// 	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package bfcpserver_test

import (
	"net"
	"testing"
	"time"

	"github.com/go-gst/go-gst/gst"
	"github.com/vopenia-io/bfcp"
)

const (
	floorTestConfID = 1
	floorTestUserID = 1
	floorTestFloor  = 1
)

// floorTestClient sends BFCP version 1 requests over UDP to a bfcpserver element.
type floorTestClient struct {
	t    *testing.T
	conn *net.UDPConn
	tid  uint16
}

func newFloorTestClient(t *testing.T) *floorTestClient {
	t.Helper()
	elem, err := gst.NewElementWithProperties("bfcpserver", map[string]interface{}{"bind-ip": "127.0.0.1"})
	if err != nil {
		t.Fatalf("bfcpserver element: %v", err)
	}
	if err := elem.SetState(gst.StateReady); err != nil {
		t.Fatalf("READY: %v", err)
	}
	t.Cleanup(func() { elem.SetState(gst.StateNull) })

	port, err := elem.GetProperty("port")
	if err != nil {
		t.Fatalf("port: %v", err)
	}
	conn, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(port.(uint))})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return &floorTestClient{t: t, conn: conn}
}

func (c *floorTestClient) send(msg *bfcp.Message) {
	c.t.Helper()
	data, err := msg.Encode()
	if err != nil {
		c.t.Fatalf("encode %s: %v", msg.Primitive, err)
	}
	if _, err := c.conn.Write(data); err != nil {
		c.t.Fatalf("send %s: %v", msg.Primitive, err)
	}
}

func (c *floorTestClient) read(within time.Duration) *bfcp.Message {
	c.t.Helper()
	buf := make([]byte, 2048)
	if err := c.conn.SetReadDeadline(time.Now().Add(within)); err != nil {
		c.t.Fatalf("deadline: %v", err)
	}
	n, err := c.conn.Read(buf)
	if err != nil {
		c.t.Fatalf("no BFCP message within %v: %v", within, err)
	}
	msg, err := bfcp.Decode(buf[:n])
	if err != nil {
		c.t.Fatalf("decode: %v", err)
	}
	return msg
}

// expectStatus reads a FloorRequestStatus and returns its floor request ID.
func (c *floorTestClient) expectStatus(within time.Duration, want bfcp.RequestStatus) uint16 {
	c.t.Helper()
	msg := c.read(within)
	if msg.Primitive != bfcp.PrimitiveFloorRequestStatus {
		code, _ := msg.GetErrorCode()
		info, _ := msg.GetErrorInfo()
		c.t.Fatalf("got %s (error=%s %q), want FloorRequestStatus %s", msg.Primitive, code, info, want)
	}
	infos := msg.FloorRequestInfos()
	if len(infos) == 0 {
		c.t.Fatalf("FloorRequestStatus without FLOOR-REQUEST-INFORMATION")
	}
	if got, _ := infos[0].Status(); got != want {
		c.t.Fatalf("FloorRequestStatus %s for request %d, want %s", got, infos[0].FloorRequestID, want)
	}
	return infos[0].FloorRequestID
}

func (c *floorTestClient) expectError(within time.Duration, want bfcp.ErrorCode) {
	c.t.Helper()
	msg := c.read(within)
	if msg.Primitive != bfcp.PrimitiveError {
		c.t.Fatalf("got %s, want Error %s", msg.Primitive, want)
	}
	if got, _ := msg.GetErrorCode(); got != want {
		c.t.Fatalf("got Error %s, want %s", got, want)
	}
}

func (c *floorTestClient) floorRequest() {
	c.tid++
	msg := bfcp.NewMessage(bfcp.PrimitiveFloorRequest, floorTestConfID, c.tid, floorTestUserID)
	msg.AddFloorID(floorTestFloor)
	c.send(msg)
}

func (c *floorTestClient) floorRelease(requestID uint16) {
	c.tid++
	msg := bfcp.NewMessage(bfcp.PrimitiveFloorRelease, floorTestConfID, c.tid, floorTestUserID)
	msg.AddFloorRequestID(requestID)
	c.send(msg)
}

// requestGranted sends a FloorRequest and returns the granted request ID.
func (c *floorTestClient) requestGranted() uint16 {
	c.t.Helper()
	c.floorRequest()
	return c.expectStatus(300*time.Millisecond, bfcp.RequestStatusGranted)
}

func TestFloorRequest_AnsweredRightAfterRelease(t *testing.T) {
	c := newFloorTestClient(t)
	for i := range 5 {
		start := time.Now()
		requestID := c.requestGranted()
		c.floorRelease(requestID)
		if got := c.expectStatus(300*time.Millisecond, bfcp.RequestStatusReleased); got != requestID {
			t.Fatalf("cycle %d: released request %d, want %d", i, got, requestID)
		}
		t.Logf("cycle %d: request %d granted and released in %v", i, requestID, time.Since(start))
	}
}

func TestFloorRequest_FromOwnerReplacesPreviousRequest(t *testing.T) {
	c := newFloorTestClient(t)

	first := c.requestGranted()
	second := c.requestGranted()
	if second == first {
		t.Fatalf("second request reused request ID %d", first)
	}

	c.floorRelease(first)
	if got := c.expectStatus(300*time.Millisecond, bfcp.RequestStatusReleased); got != first {
		t.Fatalf("released request %d, want %d", got, first)
	}
	c.floorRelease(first)
	c.expectError(300*time.Millisecond, bfcp.ErrorFloorRequestIDDoesNotExist)

	third := c.requestGranted()
	c.floorRelease(third)
	if got := c.expectStatus(300*time.Millisecond, bfcp.RequestStatusReleased); got != third {
		t.Fatalf("released request %d, want %d", got, third)
	}
}
