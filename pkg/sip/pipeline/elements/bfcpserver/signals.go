package bfcpserver

import (
	"fmt"
	"time"
	"weak"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/vopenia-io/bfcp"
)

const VirtualClientID = 0x0101
const FloorRequestDebounceDuration = 3 * time.Second

func (e *BFCPServer) startScreenshare(self *gst.Element, floorID int) {
	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Received start-screenshare signal for floorID=%d", floorID))

	floor, ok := e.bfcpServer.GetFloor(uint16(floorID))
	if !ok {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Floor not found for floorID=%d", floorID))
		self.Error(fmt.Sprintf("Floor not found for floorID=%d", floorID), fmt.Errorf("floor not found"))
		return
	}

	if floor.IsGranted() {
		if err := floor.Release(floor.GetOwner()); err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to release floor for floorID=%d: %v", floorID, err))
			self.Error(fmt.Sprintf("Failed to release floor for floorID=%d", floorID), fmt.Errorf("release failed: %v", err))
			return
		}
		e.bfcpServer.BroadcastFloorState(uint16(floorID), VirtualClientID, bfcp.RequestStatusReleased)
		time.Sleep(150 * time.Millisecond)
		self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Released existing floor for floorID=%d before starting new screenshare", floorID))
	}

	if status, err := floor.Request(VirtualClientID, uint16(e.requestID.Add(1)), bfcp.PriorityNormal); err != nil || status != bfcp.RequestStatusPending {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to request floor for floorID=%d: (%v) %v", floorID, status, err))
		self.Error(fmt.Sprintf("Failed to request floor for floorID=%d", floorID), fmt.Errorf("request failed: (%v) %v", status, err))
		return
	}

	if err := floor.Grant(); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to grant floor for floorID=%d: %v", floorID, err))
		self.Error(fmt.Sprintf("Failed to grant floor for floorID=%d", floorID), fmt.Errorf("grant failed: %v", err))
		return
	}

	e.bfcpServer.BroadcastFloorState(uint16(floorID), VirtualClientID, bfcp.RequestStatusGranted)

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Successfully requested and granted floor for floorID=%d", floorID))
}

func (e *BFCPServer) stopScreenshare(self *gst.Element, floorID int) {
	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Received stop-screenshare signal for floorID=%d", floorID))

	floor, ok := e.bfcpServer.GetFloor(uint16(floorID))
	if !ok {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Floor not found for floorID=%d", floorID))
		self.Error(fmt.Sprintf("Floor not found for floorID=%d", floorID), fmt.Errorf("floor not found"))
		return
	}

	if err := floor.Release(VirtualClientID); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to release floor for floorID=%d: %v", floorID, err))
		self.Error(fmt.Sprintf("Failed to release floor for floorID=%d", floorID), fmt.Errorf("release failed: %v", err))
		return
	}

	e.bfcpServer.BroadcastFloorState(uint16(floorID), VirtualClientID, bfcp.RequestStatusReleased)

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Successfully released floor for floorID=%d", floorID))
}

func (e *BFCPServer) SetupSignals(self *gst.Element) {
	e.bfcpServer.OnFloorRequest = func(floorID, userID, requestID uint16) bool {
		self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Floor request: floorID=%d, userID=%d, requestID=%d", floorID, userID, requestID))

		floor, ok := e.bfcpServer.GetFloor(floorID)
		if !ok {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Floor not found for floorID=%d", floorID))
			return false
		}
		if floor.IsGranted() && floor.GetOwner() != userID {
			self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Floor %d is already granted, rejecting request from userID=%d", floorID, userID))
			return false
		}

		if e.lastFloorRelease.Add(FloorRequestDebounceDuration).After(time.Now()) {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Received floor request for floorID=%d from userID=%d too soon after previous request", floorID, userID))
			time.Sleep(time.Until(e.lastFloorRelease.Add(FloorRequestDebounceDuration)))
		}

		if self.SignalHasHandlerPending(signalOnFloorRequested, glib.Quark(0), true) {
			val, err := self.Emit("on-floor-requested", int(floorID), int(userID), int(requestID))
			if err != nil {
				self.Log(CAT, gst.LevelError, fmt.Sprintf("Error emitting on-floor-requested signal: %v", err))
				self.Error("Error emitting on-floor-requested signal", err)
				return false
			}
			handled, ok := val.(bool)
			if !ok {
				self.Log(CAT, gst.LevelError, fmt.Sprintf("Invalid return type from on-floor-requested signal handler: %T", val))
				self.Error("Invalid return type from on-floor-requested signal handler", fmt.Errorf("expected bool, got %T", val))
				return false
			}
			return handled
		}

		return true
	}

	e.bfcpServer.OnFloorGranted = func(floorID, userID, requestID uint16) {
		self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Floor granted: floorID=%d, userID=%d, requestID=%d", floorID, userID, requestID))
		if _, err := self.Emit("on-floor-granted", int(floorID), int(userID), int(requestID)); err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error emitting on-floor-granted signal: %v", err))
			self.Error("Error emitting on-floor-granted signal", err)
		}
	}

	e.bfcpServer.OnFloorReleased = func(floorID, userID uint16) {
		self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Floor released: floorID=%d, userID=%d", floorID, userID))

		e.lastFloorRelease = time.Now()

		if _, err := self.Emit("on-floor-released", int(floorID), int(userID)); err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error emitting on-floor-released signal: %v", err))
			self.Error("Error emitting on-floor-released signal", err)
		}
	}

	e.bfcpServer.OnClientConnect = func(remoteAddr string, userID uint16) {
		self.Log(CAT, gst.LevelInfo, fmt.Sprintf("BFCP client connected: %s, userID=%d", remoteAddr, userID))
	}

	e.bfcpServer.OnClientDisconnect = func(remoteAddr string, userID uint16) {
		self.Log(CAT, gst.LevelInfo, fmt.Sprintf("BFCP client disconnected: %s, userID=%d", remoteAddr, userID))
	}

	eweak := weak.Make(e)
	if _, err := self.Connect("start-screenshare", func(instance *gst.Element, floorID int) {
		ptr := eweak.Value()
		if ptr != nil {
			ptr.startScreenshare(instance, floorID)
		}
	}); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to connect to start-screenshare signal: %v", err))
		self.Error("Failed to connect to start-screenshare signal", err)
	}

	if _, err := self.Connect("stop-screenshare", func(instance *gst.Element, floorID int) {
		ptr := eweak.Value()
		if ptr != nil {
			ptr.stopScreenshare(instance, floorID)
		}
	}); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to connect to stop-screenshare signal: %v", err))
		self.Error("Failed to connect to stop-screenshare signal", err)
	}

}
