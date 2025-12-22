package sip

import (
	"context"
	"fmt"
	"net/netip"
	"sync"

	"github.com/livekit/protocol/logger"
	"github.com/vopenia-io/bfcp"
)

func NewBFCPManager(ctx context.Context, log logger.Logger, opts *MediaOptions, inbound *sipInbound) *BFCPManager {
	ctx, cancel := context.WithCancel(ctx)
	b := &BFCPManager{
		log:     log.WithComponent("BFCPManager"),
		ctx:     ctx,
		cancel:  cancel,
		opts:    opts,
		inbound: inbound,
	}

	config := bfcp.DefaultServerConfig(opts.IP.String()+":0", 1)
	b.config = config
	b.config.Logger = log.WithComponent("bfcp-server")

	server := bfcp.NewServer(b.config)
	b.server = server

	if err := b.server.Listen(); err != nil {
		log.Errorw("failed to start BFCP server", err)
	}

	addr := b.server.Addr()
	b.addr = netip.MustParseAddrPort(addr.String())

	go func() { b.server.Serve() }()

	b.log.Infow("BFCP server started", "addr", b.addr.String())

	b.setup()

	return b
}

type BFCPManager struct {
	mu           sync.Mutex
	log          logger.Logger
	opts         *MediaOptions
	ctx          context.Context
	cancel       context.CancelFunc
	config       *bfcp.ServerConfig
	server       *bfcp.Server
	addr         netip.AddrPort
	inbound      *sipInbound
	orchestrator *MediaOrchestrator
}

func (b *BFCPManager) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.cancel()
	if err := b.server.Close(); err != nil {
		return fmt.Errorf("failed to close BFCP server: %w", err)
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
	b.server.OnFloorRequest = func(floorID, userID, requestID uint16) bool {
		return b.OnFloorRequest(floorID, userID, requestID)
	}

	b.server.OnFloorGranted = func(floorID, userID, requestID uint16) {
		b.OnFloorGranted(floorID, userID, requestID)
	}
}

func (b *BFCPManager) OnFloorRequest(floorID, userID, requestID uint16) bool {
	b.log.Infow("BFCP floor request received", "floorID", floorID, "userID", userID, "requestID", requestID)
	return true
}

func (b *BFCPManager) OnFloorGranted(floorID, userID, requestID uint16) {
	b.log.Infow("BFCP floor granted", "floorID", floorID, "userID", userID, "requestID", requestID)

}
