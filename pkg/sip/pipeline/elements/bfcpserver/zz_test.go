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
	"os"
	"sync"
	"testing"
	"time"

	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/bfcpserver"
	"github.com/vopenia-io/bfcp"
)

func TestMain(m *testing.M) {
	gst.Init(nil)
	bfcpserver.Register()
	os.Exit(m.Run())
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func TestClientModeAgainstUDPServer(t *testing.T) {
	config := bfcp.DefaultServerConfig("127.0.0.1:0", 1)
	config.Transport = bfcp.TransportUDP
	mcu := bfcp.NewServer(config)
	mcu.CreateFloor(0)
	if err := mcu.Listen(); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer mcu.Close()

	var mu sync.Mutex
	var connected, granted, released bool
	mcu.OnClientConnect = func(_ string, userID uint16) {
		mu.Lock()
		defer mu.Unlock()
		connected = userID == 2
	}
	mcu.OnFloorGranted = func(floorID, userID, _ uint16) {
		mu.Lock()
		defer mu.Unlock()
		granted = floorID == 0 && userID == 2
	}
	mcu.OnFloorReleased = func(floorID, userID uint16) {
		mu.Lock()
		defer mu.Unlock()
		released = floorID == 0 && userID == 2
	}
	mcu.Serve()

	elem, err := gst.NewElementWithProperties("bfcpserver", map[string]interface{}{"bind-ip": "127.0.0.1"})
	if err != nil {
		t.Fatalf("bfcpserver element: %v", err)
	}
	var remoteGranted, remoteReleased bool
	if _, err := elem.Connect("on-floor-granted", func(_ *gst.Element, floorID, userID, _ int) {
		mu.Lock()
		defer mu.Unlock()
		remoteGranted = floorID == 0 && userID == 1
	}); err != nil {
		t.Fatalf("connect on-floor-granted: %v", err)
	}
	if _, err := elem.Connect("on-floor-released", func(_ *gst.Element, floorID, userID int) {
		mu.Lock()
		defer mu.Unlock()
		remoteReleased = floorID == 0 && userID == 1
	}); err != nil {
		t.Fatalf("connect on-floor-released: %v", err)
	}
	if err := elem.SetState(gst.StateReady); err != nil {
		t.Fatalf("READY: %v", err)
	}
	defer elem.SetState(gst.StateNull)

	if _, err := elem.Emit("connect-server", mcu.Addr().String(), 1, 2, 0, 2); err != nil {
		t.Fatalf("connect-server: %v", err)
	}
	waitFor(t, "Hello at the MCU", func() bool { mu.Lock(); defer mu.Unlock(); return connected })

	if _, err := elem.Emit("start-screenshare", 0); err != nil {
		t.Fatalf("start-screenshare: %v", err)
	}
	waitFor(t, "floor granted at the MCU", func() bool { mu.Lock(); defer mu.Unlock(); return granted })

	if _, err := elem.Emit("stop-screenshare", 0); err != nil {
		t.Fatalf("stop-screenshare: %v", err)
	}
	waitFor(t, "floor released at the MCU", func() bool { mu.Lock(); defer mu.Unlock(); return released })

	mcu.BroadcastFloorState(0, 7, bfcp.RequestStatusGranted)
	waitFor(t, "remote share reported", func() bool { mu.Lock(); defer mu.Unlock(); return remoteGranted })
	mcu.BroadcastFloorState(0, 7, bfcp.RequestStatusReleased)
	waitFor(t, "remote share end reported", func() bool { mu.Lock(); defer mu.Unlock(); return remoteReleased })
}
