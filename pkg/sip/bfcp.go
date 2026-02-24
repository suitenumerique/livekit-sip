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

// VirtualClientUserID is the BFCP user ID for the virtual client representing WebRTC participants.
const VirtualClientUserID uint16 = 65534

// ContentFloorID is the default floor ID for screenshare/content.
const ContentFloorID uint16 = 1

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

	client     *bfcp.Client
	isActive   bool
	remoteAddr string

	virtualFloorHeld bool
	virtualRequestID uint16

	OnFloorGranted  func(floorID, userID uint16)
	OnFloorReleased func(floorID, userID uint16)
}

// NewBFCPManager creates a new BFCP manager in active or passive mode based on the remote's setup attribute.
func NewBFCPManager(ctx context.Context, log logger.Logger, opts *MediaOptions, transport BFCPTransport, bfcpOffer *sdpv2.SDPBfcp, sessionAddr netip.Addr) *BFCPManager {
	ctx, cancel := context.WithCancel(ctx)
	b := &BFCPManager{
		log:       log.WithComponent("BFCPManager"),
		ctx:       ctx,
		cancel:    cancel,
		opts:      opts,
		transport: transport,
	}

	if bfcpOffer != nil && bfcpOffer.Setup == sdpv2.BfcpSetupPassive {
		// Active mode: remote is passive, we connect to them
		b.isActive = true
		addr := bfcpOffer.ConnectionAddr
		if !addr.IsValid() {
			addr = sessionAddr
		}
		b.remoteAddr = fmt.Sprintf("%s:%d", addr, bfcpOffer.Port)
		log.Infow("BFCP using active mode (we connect to remote)", "remoteAddr", b.remoteAddr)

		config := bfcp.DefaultServerConfig("0.0.0.0:0", 1)
		config.PortMin = opts.Ports.Start
		config.PortMax = opts.Ports.End
		if bfcpOffer != nil {
			config.ConferenceID = bfcpOffer.ConfID
		}
		b.config = config

		// Allocate a local port for the SDP answer
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
		server.Close()
		b.server = nil

		log.Infow("BFCP active mode initialized", "localPort", b.addr.Port(), "remoteAddr", b.remoteAddr)
	} else {
		// Passive mode: we listen for connections
		b.isActive = false
		log.Infow("BFCP using passive mode (we listen for connections)")

		config := bfcp.DefaultServerConfig("0.0.0.0:0", 1)
		config.PortMin = opts.Ports.Start
		config.PortMax = opts.Ports.End
		config.AutoGrant = true
		config.Logger = log.WithComponent("bfcp-server")

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

		b.server.CreateFloor(ContentFloorID)

		if err := b.server.Listen(); err != nil {
			log.Errorw("failed to start BFCP server, BFCP disabled", err)
			cancel()
			return nil
		}

		addr := b.server.Addr()
		b.addr = netip.MustParseAddrPort(addr.String())

		go func() {
			b.server.Serve()
		}()

		b.log.Infow("BFCP server started", "addr", b.addr.String(), "transport", transport)

		b.setupServerCallbacks()
	}

	return b
}

func (b *BFCPManager) Transport() BFCPTransport {
	return b.transport
}

func (b *BFCPManager) IsActive() bool {
	return b.isActive
}

// ConnectToRemote connects to the remote BFCP server in active mode. No-op in passive mode.
func (b *BFCPManager) ConnectToRemote() error {
	if !b.isActive {
		return nil
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if b.client != nil {
		return nil
	}

	b.log.Infow("bfcp.client.connecting", "addr", b.remoteAddr)

	config := bfcp.DefaultClientConfig(b.remoteAddr, b.config.ConferenceID, 1)
	config.Logger = b.log

	client := bfcp.NewClient(config)
	b.client = client

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

	if b.client != nil {
		if err := b.client.Disconnect(); err != nil {
			b.log.Warnw("failed to disconnect BFCP client", err)
		}
		b.client = nil
	}

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

	b.server.OnFloorRequest = func(floorID, userID, requestID uint16) bool {
		b.log.Infow("bfcp.floor_request", "floorID", floorID, "userID", userID, "requestID", requestID)
		return true
	}

	b.server.OnFloorGranted = func(floorID, userID, requestID uint16) {
		b.log.Infow("bfcp.floor_granted", "floorID", floorID, "userID", userID, "requestID", requestID)
		if b.OnFloorGranted != nil {
			b.OnFloorGranted(floorID, userID)
		}
	}

	b.server.OnFloorReleased = func(floorID, userID uint16) {
		b.log.Infow("bfcp.floor_released", "floorID", floorID, "userID", userID)
		if b.OnFloorReleased != nil {
			b.OnFloorReleased(floorID, userID)
		}
	}

	b.server.OnClientConnect = func(remoteAddr string, userID uint16) {
		b.log.Debugw("bfcp.client.connect", "remote", remoteAddr, "userID", userID)
	}

	b.server.OnClientDisconnect = func(remoteAddr string, userID uint16) {
		b.log.Infow("bfcp.client.disconnect", "remote", remoteAddr, "userID", userID)
	}

	b.server.OnError = func(err error) {
		b.log.Errorw("bfcp.server.error", err)
	}
}

// RequestFloorForVirtualClient requests the content floor on behalf of the WebRTC screenshare participant.
func (b *BFCPManager) RequestFloorForVirtualClient() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.virtualFloorHeld {
		b.log.Debugw("bfcp.webrtc.floor_already_held")
		return nil
	}

	b.log.Infow("bfcp.webrtc.floor_request")

	if b.isActive && b.client != nil {
		_, err := b.client.RequestFloor(ContentFloorID, 0, bfcp.PriorityNormal)
		if err != nil {
			b.log.Errorw("bfcp.client.floor_request_failed", err)
			return fmt.Errorf("floor request failed: %w", err)
		}
		return nil
	}

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

	b.server.BroadcastFloorState(ContentFloorID, VirtualClientUserID, bfcp.RequestStatusGranted)

	return nil
}

// ReleaseFloorForVirtualClient releases the content floor held by the WebRTC screenshare participant.
func (b *BFCPManager) ReleaseFloorForVirtualClient() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if !b.virtualFloorHeld {
		b.log.Debugw("bfcp.webrtc.floor_not_held")
		return nil
	}

	b.log.Infow("bfcp.webrtc.floor_releasing")

	if b.isActive && b.client != nil {
		if err := b.client.ReleaseFloor(ContentFloorID); err != nil {
			b.log.Errorw("bfcp.client.floor_release_failed", err)
			return fmt.Errorf("floor release failed: %w", err)
		}
		b.virtualFloorHeld = false
		b.log.Infow("bfcp.webrtc.floor_released")
		return nil
	}

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

	b.server.BroadcastFloorState(ContentFloorID, VirtualClientUserID, bfcp.RequestStatusReleased)

	return nil
}

func (b *BFCPManager) IsVirtualClientHoldingFloor() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.virtualFloorHeld
}
