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

package bfcpserver

import (
	"fmt"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/vopenia-io/bfcp"
)

const remoteUserID = 1

func (e *BFCPServer) udpClient() *bfcp.UDPClient {
	e.clientMu.Lock()
	defer e.clientMu.Unlock()
	return e.client
}

func (e *BFCPServer) connectServer(self *gst.Element, remoteAddr string, confID, userID, floorID, version int) {
	client, err := e.bfcpServer.NewUDPClient(bfcp.UDPClientConfig{
		RemoteAddr:   remoteAddr,
		ConferenceID: uint32(confID),
		UserID:       uint16(userID),
		Version:      uint8(version),
	})
	if err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to create BFCP client\nremote=%s\nerr=%v", remoteAddr, err))
		return
	}

	wself := glib.WeakRefInit(self)
	client.OnFloorGranted = func(floorID, requestID uint16) {
		if self := gst.ToElement(wself.Get()); self != nil {
			self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Floor granted by remote server\nfloor_id=%d\nrequest_id=%d", floorID, requestID))
		}
	}
	client.OnFloorDenied = func(floorID, requestID uint16) {
		if self := gst.ToElement(wself.Get()); self != nil {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Floor denied by remote server\nfloor_id=%d\nrequest_id=%d", floorID, requestID))
		}
	}
	client.OnFloorReleased = func(floorID uint16) {
		if self := gst.ToElement(wself.Get()); self != nil {
			self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Own floor released on remote server\nfloor_id=%d", floorID))
		}
	}
	client.OnFloorStatus = func(floorID, beneficiaryID uint16, status bfcp.RequestStatus) {
		self := gst.ToElement(wself.Get())
		if self == nil {
			return
		}
		e.remoteFloorStatus(self, floorID, beneficiaryID, status)
	}
	client.OnError = func(err error) {
		if self := gst.ToElement(wself.Get()); self != nil {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("BFCP error from remote server\nerr=%v", err))
		}
	}

	e.clientMu.Lock()
	previous := e.client
	e.client = client
	e.clientMu.Unlock()
	if previous != nil {
		previous.Close()
	}
	e.expectedPeer.Store(&remoteAddr)

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Connecting to remote BFCP server\nremote=%s\nconf_id=%d\nuser_id=%d\nfloor_id=%d\nversion=%d", remoteAddr, confID, userID, floorID, version))

	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		if err := client.Hello(); err != nil {
			if self := gst.ToElement(wself.Get()); self != nil {
				self.Log(CAT, gst.LevelWarning, fmt.Sprintf("BFCP Hello to remote server failed\nremote=%s\nerr=%v", remoteAddr, err))
			}
		}
	}()
}

func (e *BFCPServer) remoteFloorStatus(self *gst.Element, floorID, beneficiaryID uint16, status bfcp.RequestStatus) {
	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Remote floor status\nfloor_id=%d\nbeneficiary_id=%d\nstatus=%s", floorID, beneficiaryID, status.String()))

	switch status {
	case bfcp.RequestStatusGranted:
		if _, err := self.Emit("on-floor-requested", int(floorID), remoteUserID, int(beneficiaryID)); err != nil {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to emit on-floor-requested signal\nerr=%v", err))
		}
		if _, err := self.Emit("on-floor-granted", int(floorID), remoteUserID, int(beneficiaryID)); err != nil {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to emit on-floor-granted signal\nerr=%v", err))
		}
	case bfcp.RequestStatusReleased, bfcp.RequestStatusRevoked, bfcp.RequestStatusCancelled:
		if _, err := self.Emit("on-floor-released", int(floorID), remoteUserID); err != nil {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to emit on-floor-released signal\nerr=%v", err))
		}
	}
}

func (e *BFCPServer) clientStartScreenshare(self *gst.Element, client *bfcp.UDPClient, floorID int) {
	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Requesting floor from remote server\nfloor_id=%d", floorID))
	wself := glib.WeakRefInit(self)
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		if _, err := client.RequestFloor(uint16(floorID)); err != nil {
			if self := gst.ToElement(wself.Get()); self != nil {
				self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Floor request to remote server failed\nfloor_id=%d\nerr=%v", floorID, err))
			}
		}
	}()
}

func (e *BFCPServer) clientStopScreenshare(self *gst.Element, client *bfcp.UDPClient, floorID int) {
	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Releasing floor on remote server\nfloor_id=%d", floorID))
	wself := glib.WeakRefInit(self)
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		if err := client.ReleaseFloor(uint16(floorID)); err != nil {
			if self := gst.ToElement(wself.Get()); self != nil {
				self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Floor release on remote server failed\nfloor_id=%d\nerr=%v", floorID, err))
			}
		}
	}()
}

func (e *BFCPServer) closeClient() {
	e.clientMu.Lock()
	client := e.client
	e.client = nil
	e.clientMu.Unlock()
	if client != nil {
		client.Close()
	}
}
