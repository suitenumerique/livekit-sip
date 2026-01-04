package sip

import (
	"context"
	"fmt"
	"net/netip"
	"sync"

	"github.com/livekit/protocol/logger"
	"github.com/vopenia-io/bfcp"
)

// VirtualClientUserID is the user ID used for the virtual BFCP client
// representing WebRTC participants. This is used when WebRTC shares screen.
const VirtualClientUserID uint16 = 65534

// ContentFloorID is the default floor ID for screenshare/content
const ContentFloorID uint16 = 1

type BFCPManager struct {
	mu     sync.Mutex
	log    logger.Logger
	opts   *MediaOptions
	ctx    context.Context
	cancel context.CancelFunc
	config *bfcp.ServerConfig
	server *bfcp.Server
	addr   netip.AddrPort

	// Track virtual client floor state (WebRTC side)
	virtualFloorHeld bool
	virtualRequestID uint16

	// Callbacks for floor events - set by MediaOrchestrator
	OnFloorGranted  func(floorID, userID uint16)
	OnFloorReleased func(floorID, userID uint16)
}

func NewBFCPManager(ctx context.Context, log logger.Logger, opts *MediaOptions) *BFCPManager {
	ctx, cancel := context.WithCancel(ctx)
	b := &BFCPManager{
		log:    log.WithComponent("BFCPManager"),
		ctx:    ctx,
		cancel: cancel,
		opts:   opts,
	}

	config := bfcp.DefaultServerConfig(opts.IP.String()+":0", 1)
	config.AutoGrant = true // Auto-grant floor requests for 1:1 calls
	config.Logger = log.WithComponent("bfcp-server")
	b.config = config

	server := bfcp.NewServer(config)
	b.server = server

	// Create the content floor
	b.server.CreateFloor(ContentFloorID)

	if err := b.server.Listen(); err != nil {
		log.Errorw("failed to start BFCP server, BFCP disabled", err)
		cancel()
		return nil
	}

	addr := b.server.Addr()
	b.addr = netip.MustParseAddrPort(addr.String())

	// Start server in goroutine
	go func() {
		b.server.Serve()
	}()

	b.log.Infow("BFCP server started", "addr", b.addr.String())

	b.setup()

	return b
}

func (b *BFCPManager) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.cancel()
	if b.server != nil {
		if err := b.server.Close(); err != nil {
			return fmt.Errorf("failed to close BFCP server: %w", err)
		}
	}
	return nil
}

func (b *BFCPManager) Port() uint16 {
	return b.addr.Port()
}

func (b *BFCPManager) Address() netip.Addr {
	return b.addr.Addr()
}

func (b *BFCPManager) setup() {
	// Handle incoming floor requests from Poly
	b.server.OnFloorRequest = func(floorID, userID, requestID uint16) bool {
		b.log.Infow("bfcp.poly.floor_request", "floorID", floorID, "userID", userID, "requestID", requestID)
		// Auto-grant is enabled, so this returns true
		return true
	}

	// Handle floor granted events
	b.server.OnFloorGranted = func(floorID, userID, requestID uint16) {
		b.log.Infow("bfcp.floor_granted", "floorID", floorID, "userID", userID, "requestID", requestID)
		if b.OnFloorGranted != nil {
			b.OnFloorGranted(floorID, userID)
		}
	}

	// Handle floor released events
	b.server.OnFloorReleased = func(floorID, userID uint16) {
		b.log.Infow("bfcp.floor_released", "floorID", floorID, "userID", userID)
		if b.OnFloorReleased != nil {
			b.OnFloorReleased(floorID, userID)
		}
	}

	// Handle client connections (Poly connects to our BFCP server)
	b.server.OnClientConnect = func(remoteAddr string, userID uint16) {
		b.log.Infow("bfcp.client.connect", "remote", remoteAddr, "userID", userID)
	}

	b.server.OnClientDisconnect = func(remoteAddr string, userID uint16) {
		b.log.Infow("bfcp.client.disconnect", "remote", remoteAddr, "userID", userID)
	}

	b.server.OnError = func(err error) {
		b.log.Errorw("bfcp.server.error", err)
	}
}

// RequestFloorForVirtualClient requests the content floor on behalf of the
// virtual BFCP client (representing WebRTC participant starting screenshare).
// This notifies Poly that we are now presenting content.
func (b *BFCPManager) RequestFloorForVirtualClient() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.virtualFloorHeld {
		b.log.Debugw("bfcp.webrtc.floor_already_held")
		return nil
	}

	b.log.Infow("bfcp.webrtc.floor_request")

	floor, exists := b.server.GetFloor(ContentFloorID)
	if !exists {
		floor = b.server.CreateFloor(ContentFloorID)
	}

	b.virtualRequestID++
	status, err := floor.Request(VirtualClientUserID, b.virtualRequestID, bfcp.PriorityNormal)
	if err != nil {
		b.log.Errorw("bfcp.webrtc.floor_request_failed", err)
		return fmt.Errorf("floor request failed: %w", err)
	}

	b.log.Infow("bfcp.webrtc.floor_request_status", "status", status.String())

	// Auto-grant for virtual client
	if status == bfcp.RequestStatusPending || status == bfcp.RequestStatusAccepted {
		if err := floor.Grant(); err != nil {
			b.log.Errorw("bfcp.webrtc.floor_grant_failed", err)
			return fmt.Errorf("floor grant failed: %w", err)
		}
		b.log.Infow("bfcp.webrtc.floor_granted")

		if b.OnFloorGranted != nil {
			b.OnFloorGranted(ContentFloorID, VirtualClientUserID)
		}
	}

	b.virtualFloorHeld = true

	// Broadcast FloorStatus to connected BFCP clients (Poly) to notify them
	// that someone (our virtual client) is now presenting
	b.server.BroadcastFloorState(ContentFloorID, VirtualClientUserID, bfcp.RequestStatusGranted)

	return nil
}

// ReleaseFloorForVirtualClient releases the content floor held by the
// virtual BFCP client (when WebRTC screenshare stops).
func (b *BFCPManager) ReleaseFloorForVirtualClient() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if !b.virtualFloorHeld {
		b.log.Debugw("bfcp.webrtc.floor_not_held")
		return nil
	}

	b.log.Infow("bfcp.webrtc.floor_releasing")

	floor, exists := b.server.GetFloor(ContentFloorID)
	if !exists {
		b.virtualFloorHeld = false
		return nil
	}

	if err := floor.Release(VirtualClientUserID); err != nil {
		b.log.Errorw("bfcp.webrtc.floor_release_failed", err)
		return fmt.Errorf("floor release failed: %w", err)
	}

	b.virtualFloorHeld = false
	b.log.Infow("bfcp.webrtc.floor_released")

	// Broadcast FloorStatus to connected BFCP clients (Poly)
	b.server.BroadcastFloorState(ContentFloorID, VirtualClientUserID, bfcp.RequestStatusReleased)

	return nil
}

// IsVirtualClientHoldingFloor returns true if the WebRTC side is currently presenting.
func (b *BFCPManager) IsVirtualClientHoldingFloor() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.virtualFloorHeld
}
