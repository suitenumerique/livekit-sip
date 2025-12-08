package bfcp

import (
	"sync"
	"sync/atomic"

	"github.com/livekit/protocol/logger"
)

// FloorState represents the state of a floor for a virtual client.
type FloorState struct {
	FloorID   uint16
	HasFloor  bool
	RequestID uint16
	Pending   bool // Request is pending (waiting for grant/deny)
}

// VirtualClient represents a WebRTC participant's floor state.
// It does not communicate via BFCP protocol - it's purely state tracking
// for coordinating floor control between SIP and WebRTC sides.
type VirtualClient struct {
	userID uint16
	log    logger.Logger

	mu          sync.RWMutex
	floorStates map[uint16]*FloorState

	requestIDCounter uint32
}

// NewVirtualClient creates a new virtual BFCP client for a WebRTC participant.
func NewVirtualClient(log logger.Logger, userID uint16) *VirtualClient {
	c := &VirtualClient{
		userID:      userID,
		log:         log,
		floorStates: make(map[uint16]*FloorState),
	}

	log.Debugw("bfcp.virtual.created", "userID", userID)

	return c
}

// UserID returns the user ID of this virtual client.
func (c *VirtualClient) UserID() uint16 {
	return c.userID
}

// RequestFloor simulates a floor request from the WebRTC side.
// Returns a request ID that can be used to track the request.
func (c *VirtualClient) RequestFloor(floorID uint16) uint16 {
	c.mu.Lock()
	defer c.mu.Unlock()

	requestID := uint16(atomic.AddUint32(&c.requestIDCounter, 1))

	state, exists := c.floorStates[floorID]
	if !exists {
		state = &FloorState{FloorID: floorID}
		c.floorStates[floorID] = state
	}

	state.RequestID = requestID
	state.Pending = true

	c.log.Debugw("bfcp.virtual.floor_request", "floorID", floorID, "requestID", requestID)

	return requestID
}

// ReleaseFloor simulates releasing a floor from the WebRTC side.
func (c *VirtualClient) ReleaseFloor(floorID uint16) {
	c.mu.Lock()
	defer c.mu.Unlock()

	state, exists := c.floorStates[floorID]
	if !exists {
		return
	}

	c.log.Debugw("bfcp.virtual.floor_release", "floorID", floorID)

	state.HasFloor = false
	state.Pending = false
	state.RequestID = 0
}

// SetFloorGranted updates the floor state when granted or denied.
func (c *VirtualClient) SetFloorGranted(floorID uint16, granted bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	state, exists := c.floorStates[floorID]
	if !exists {
		state = &FloorState{FloorID: floorID}
		c.floorStates[floorID] = state
	}

	state.HasFloor = granted
	state.Pending = false

	c.log.Debugw("bfcp.virtual.floor_state", "floorID", floorID, "granted", granted)
}

// HasFloor returns true if the virtual client currently holds the specified floor.
func (c *VirtualClient) HasFloor(floorID uint16) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	state, exists := c.floorStates[floorID]
	if !exists {
		return false
	}
	return state.HasFloor
}

// IsPending returns true if there's a pending request for the specified floor.
func (c *VirtualClient) IsPending(floorID uint16) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	state, exists := c.floorStates[floorID]
	if !exists {
		return false
	}
	return state.Pending
}

// GetFloorState returns a copy of the floor state for the specified floor.
func (c *VirtualClient) GetFloorState(floorID uint16) *FloorState {
	c.mu.RLock()
	defer c.mu.RUnlock()

	state, exists := c.floorStates[floorID]
	if !exists {
		return nil
	}

	// Return a copy
	return &FloorState{
		FloorID:   state.FloorID,
		HasFloor:  state.HasFloor,
		RequestID: state.RequestID,
		Pending:   state.Pending,
	}
}

// GetAllFloorStates returns a copy of all floor states.
func (c *VirtualClient) GetAllFloorStates() map[uint16]*FloorState {
	c.mu.RLock()
	defer c.mu.RUnlock()

	result := make(map[uint16]*FloorState, len(c.floorStates))
	for k, v := range c.floorStates {
		result[k] = &FloorState{
			FloorID:   v.FloorID,
			HasFloor:  v.HasFloor,
			RequestID: v.RequestID,
			Pending:   v.Pending,
		}
	}
	return result
}

// ReleaseAllFloors releases all floors held by this virtual client.
func (c *VirtualClient) ReleaseAllFloors() {
	c.mu.Lock()
	defer c.mu.Unlock()

	for floorID, state := range c.floorStates {
		if state.HasFloor {
			c.log.Debugw("bfcp.virtual.floor_release_cleanup", "floorID", floorID)
		}
		state.HasFloor = false
		state.Pending = false
		state.RequestID = 0
	}
}
