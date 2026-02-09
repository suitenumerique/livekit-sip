package sip

import (
	"context"
	"fmt"
	"net/netip"
	"sync"

	sdpv2 "github.com/livekit/media-sdk/sdp/v2"
	"github.com/livekit/protocol/logger"
	"github.com/vopenia-io/bfcp"
)

// VirtualClientUserID is the user ID used for the virtual BFCP client
// representing WebRTC participants. This is used when WebRTC shares screen.
const VirtualClientUserID uint16 = 65534

// ContentFloorID is the default floor ID for screenshare/content
const ContentFloorID uint16 = 1

// BFCPTransport specifies the BFCP transport protocol
type BFCPTransport int

const (
	BFCPTransportTCP BFCPTransport = iota // TCP transport (RFC 4582) - used by Poly
	BFCPTransportUDP                      // UDP transport (RFC 8855) - used by Cisco
)

type BFCPManager struct {
	mu        sync.Mutex
	log       logger.Logger
	opts      *MediaOptions
	ctx       context.Context
	cancel    context.CancelFunc
	config    *bfcp.ServerConfig
	server    *bfcp.Server
	addr      netip.AddrPort
	transport BFCPTransport

	// Client mode support (when we need to connect to remote BFCP server)
	client     *bfcp.Client // BFCP client for active mode
	isActive   bool         // true = we connect to them (active), false = they connect to us (passive)
	remoteAddr string       // remote BFCP server address (for active mode)

	// Track virtual client floor state (WebRTC side)
	virtualFloorHeld bool
	virtualRequestID uint16

	// Callbacks for floor events - set by MediaOrchestrator
	OnFloorGranted  func(floorID, userID uint16)
	OnFloorReleased func(floorID, userID uint16)
}

// NewBFCPManager creates a new BFCP manager.
// If bfcpOffer.Setup is "passive", we become "active" (connect to them).
// If bfcpOffer.Setup is "actpass" or "active", we become "passive" (they connect to us).
func NewBFCPManager(ctx context.Context, log logger.Logger, opts *MediaOptions, transport BFCPTransport, bfcpOffer *sdpv2.SDPBfcp, sessionAddr netip.Addr) *BFCPManager {
	ctx, cancel := context.WithCancel(ctx)
	b := &BFCPManager{
		log:       log.WithComponent("BFCPManager"),
		ctx:       ctx,
		cancel:    cancel,
		opts:      opts,
		transport: transport,
	}

	// Determine if we should be active (client) or passive (server)
	// based on the remote's setup attribute
	if bfcpOffer != nil && bfcpOffer.Setup == sdpv2.BfcpSetupPassive {
		// Remote is passive (server), so we must be active (client)
		b.isActive = true
		addr := bfcpOffer.ConnectionAddr
		if !addr.IsValid() {
			addr = sessionAddr // fallback to session-level c= address
		}
		b.remoteAddr = fmt.Sprintf("%s:%d", addr, bfcpOffer.Port)
		log.Infow("BFCP using active mode (we connect to remote)", "remoteAddr", b.remoteAddr)

		// Still create server config for the port allocation
		config := bfcp.DefaultServerConfig("0.0.0.0:0", 1)
		config.PortMin = opts.Ports.Start
		config.PortMax = opts.Ports.End
		if bfcpOffer != nil {
			config.ConferenceID = bfcpOffer.ConfID
		}
		b.config = config

		// Allocate a local port for SDP answer (even though we won't listen)
		// We need to report a port in our SDP answer
		server := bfcp.NewServer(config)
		if transport == BFCPTransportUDP {
			config.Transport = bfcp.TransportUDP
		} else {
			config.Transport = bfcp.TransportTCP
		}
		if err := server.Listen(); err != nil {
			log.Errorw("failed to allocate BFCP port", err)
			cancel()
			return nil
		}
		srvAddr := server.Addr()
		b.addr = netip.MustParseAddrPort(srvAddr.String())
		// Close the server immediately - we won't use it in active mode
		server.Close()
		b.server = nil

		log.Infow("BFCP active mode initialized", "localPort", b.addr.Port(), "remoteAddr", b.remoteAddr)
	} else {
		// Remote is actpass or active, so we are passive (server)
		b.isActive = false
		log.Infow("BFCP using passive mode (we listen for connections)")

		config := bfcp.DefaultServerConfig("0.0.0.0:0", 1)
		config.PortMin = opts.Ports.Start
		config.PortMax = opts.Ports.End
		config.AutoGrant = true // Auto-grant floor requests for 1:1 calls
		config.Logger = log.WithComponent("bfcp-server")

		// Set transport type based on SDP offer (UDP for Cisco, TCP for Poly)
		if transport == BFCPTransportUDP {
			config.Transport = bfcp.TransportUDP
			log.Infow("BFCP using UDP transport (RFC 8855)")
		} else {
			config.Transport = bfcp.TransportTCP
			log.Infow("BFCP using TCP transport (RFC 4582)")
		}

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

		b.log.Infow("BFCP server started", "addr", b.addr.String(), "transport", transport)

		b.setupServerCallbacks()
	}

	return b
}

// Transport returns the BFCP transport type (TCP or UDP)
func (b *BFCPManager) Transport() BFCPTransport {
	return b.transport
}

// IsActive returns true if we are in active mode (we connect to remote)
func (b *BFCPManager) IsActive() bool {
	return b.isActive
}

// ConnectToRemote connects to the remote BFCP server (only in active mode).
// This should be called after the SDP exchange is complete.
func (b *BFCPManager) ConnectToRemote() error {
	if !b.isActive {
		// We're passive, nothing to do - remote will connect to us
		return nil
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if b.client != nil {
		// Already connected
		return nil
	}

	b.log.Infow("bfcp.client.connecting", "addr", b.remoteAddr)

	config := bfcp.DefaultClientConfig(b.remoteAddr, b.config.ConferenceID, 1)
	config.Logger = b.log

	client := bfcp.NewClient(config)
	b.client = client

	// Wire up callbacks
	client.OnConnected = func() {
		b.log.Infow("bfcp.client.connected", "addr", b.remoteAddr)
	}

	client.OnDisconnected = func() {
		b.log.Infow("bfcp.client.disconnected", "addr", b.remoteAddr)
	}

	client.OnFloorGranted = func(floorID, requestID uint16) {
		b.log.Infow("bfcp.client.floor_granted", "floorID", floorID, "requestID", requestID)
		b.virtualFloorHeld = true
		if b.OnFloorGranted != nil {
			b.OnFloorGranted(floorID, VirtualClientUserID)
		}
	}

	client.OnFloorReleased = func(floorID uint16) {
		b.log.Infow("bfcp.client.floor_released", "floorID", floorID)
		b.virtualFloorHeld = false
		if b.OnFloorReleased != nil {
			b.OnFloorReleased(floorID, VirtualClientUserID)
		}
	}

	client.OnFloorDenied = func(floorID, requestID uint16, errorCode bfcp.ErrorCode) {
		b.log.Warnw("bfcp.client.floor_denied", nil, "floorID", floorID, "requestID", requestID, "errorCode", errorCode)
	}

	client.OnError = func(err error) {
		b.log.Errorw("bfcp.client.error", err)
	}

	if err := client.Connect(); err != nil {
		b.log.Errorw("bfcp.client.connect_failed", err, "addr", b.remoteAddr)
		return fmt.Errorf("failed to connect to BFCP server: %w", err)
	}

	// Send Hello to establish BFCP session
	if err := client.Hello(); err != nil {
		b.log.Errorw("bfcp.client.hello_failed", err)
		return fmt.Errorf("BFCP Hello failed: %w", err)
	}

	b.log.Infow("bfcp.client.ready", "addr", b.remoteAddr)

	return nil
}

func (b *BFCPManager) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.cancel()

	// Close client if in active mode
	if b.client != nil {
		if err := b.client.Disconnect(); err != nil {
			b.log.Warnw("failed to disconnect BFCP client", err)
		}
		b.client = nil
	}

	// Close server if in passive mode
	if b.server != nil {
		if err := b.server.Close(); err != nil {
			return fmt.Errorf("failed to close BFCP server: %w", err)
		}
		b.server = nil
	}

	return nil
}

func (b *BFCPManager) Port() uint16 {
	return b.addr.Port()
}

func (b *BFCPManager) Address() netip.Addr {
	return b.addr.Addr()
}

func (b *BFCPManager) setupServerCallbacks() {
	if b.server == nil {
		return
	}

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
// This notifies the remote endpoint that we are now presenting content.
func (b *BFCPManager) RequestFloorForVirtualClient() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.virtualFloorHeld {
		b.log.Debugw("bfcp.webrtc.floor_already_held")
		return nil
	}

	b.log.Infow("bfcp.webrtc.floor_request")

	// Active mode: use client to request floor from remote server
	if b.isActive && b.client != nil {
		_, err := b.client.RequestFloor(ContentFloorID, 0, bfcp.PriorityNormal)
		if err != nil {
			b.log.Errorw("bfcp.client.floor_request_failed", err)
			return fmt.Errorf("floor request failed: %w", err)
		}
		// Floor granted callback will set virtualFloorHeld = true
		return nil
	}

	// Passive mode: use server to grant floor locally
	if b.server == nil {
		return fmt.Errorf("BFCP server not initialized")
	}

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

	// Active mode: use client to release floor on remote server
	if b.isActive && b.client != nil {
		if err := b.client.ReleaseFloor(ContentFloorID); err != nil {
			b.log.Errorw("bfcp.client.floor_release_failed", err)
			return fmt.Errorf("floor release failed: %w", err)
		}
		b.virtualFloorHeld = false
		b.log.Infow("bfcp.webrtc.floor_released")
		return nil
	}

	// Passive mode: use server to release floor locally
	if b.server == nil {
		b.virtualFloorHeld = false
		return nil
	}

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
