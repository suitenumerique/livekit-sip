package sip

import (
	"context"
	"fmt"
	"net/netip"
	"sync/atomic"

	sdpv2 "github.com/livekit/media-sdk/sdp/v2"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/sip/pkg/sip/pipeline/screenshare_pipeline"
)

type ScreenshareManager struct {
	*VideoManager
	active    atomic.Bool
	floorHeld atomic.Bool

	remoteAddr      netip.Addr      // SDP remote address
	pendingMedia    *sdpv2.SDPMedia // Media for pipeline creation
	lastKnownMedia  *sdpv2.SDPMedia // SDP media for pipeline restart
	trackInput      *TrackInput     // Track reference for cleanup
	pendingTrack    *TrackInput     // Track awaiting pipeline start
	pendingTrackSID string

	OnScreenshareStarted func()
	OnScreenshareStopped func()
}

func NewScreenshareManager(log logger.Logger, ctx context.Context, opts *MediaOptions) (*ScreenshareManager, error) {
	sm := &ScreenshareManager{}

	vm, err := NewVideoManager(log.WithComponent("screenshare"), ctx, opts, sm)
	if err != nil {
		return nil, err
	}
	sm.VideoManager = vm

	return sm, nil
}

func (sm *ScreenshareManager) CreateVideoPipeline(opt *MediaOptions) (SipPipeline, error) {
	media := sm.pendingMedia
	if media == nil || media.Codec == nil {
		return nil, fmt.Errorf("screenshare media not configured")
	}

	pipeline, err := screenshare_pipeline.New(sm.ctx, sm.log, media.Codec.PayloadType)
	if err != nil {
		return nil, fmt.Errorf("failed to create screenshare pipeline: %w", err)
	}

	pipeline.OnTrackEnded = sm.handleWebrtcTrackEnded

	return pipeline, nil
}

func (sm *ScreenshareManager) Reconcile(remote netip.Addr, media *sdpv2.SDPMedia) (ReconcileStatus, error) {
	sm.remoteAddr = remote
	sm.pendingMedia = media

	isRealScreenshare := media.Content == sdpv2.ContentTypeSlides && media.Port > 0

	// Update RTP destination from SDP if pipeline already active
	if isRealScreenshare && sm.status >= VideoStatusReady {
		sm.log.Infow("screenshare.reconcile: updating destination",
			"rtpDst", netip.AddrPortFrom(remote, media.Port))
		sm.rtpConn.SetDst(netip.AddrPortFrom(remote, media.Port))
		sm.rtcpConn.SetDst(netip.AddrPortFrom(remote, media.RTCPPort))
		sm.Media = media
		mediaCopy := *media
		sm.lastKnownMedia = &mediaCopy
		sm.pendingMedia = nil
		return ReconcileStatusUpdated, nil
	}

	// Placeholder port for initial setup before re-INVITE
	screenshareMedia := *media
	if !isRealScreenshare {
		screenshareMedia.Port = 1
		screenshareMedia.RTCPPort = 1
	}

	rs, err := sm.VideoManager.Reconcile(remote, &screenshareMedia)
	if err != nil {
		return rs, err
	}

	mediaCopy := *media
	sm.lastKnownMedia = &mediaCopy
	sm.pendingMedia = nil

	return rs, nil
}

func (sm *ScreenshareManager) Start() error {
	if err := sm.VideoManager.Start(); err != nil {
		return err
	}

	// Process pending track (best-effort)
	if sm.pendingTrack != nil {
		ti := sm.pendingTrack
		sid := sm.pendingTrackSID
		sm.pendingTrack = nil
		sm.pendingTrackSID = ""

		sm.log.Infow("screenshare.start: processing pending track", "sid", sid)
		if err := sm.processTrackInput(ti, sid, 0); err != nil {
			sm.log.Warnw("failed to process pending track", err, "sid", sid)
		}
	}

	return nil
}

// WebrtcTrackInput sets the WebRTC screenshare track.
func (sm *ScreenshareManager) WebrtcTrackInput(ti *TrackInput, sid string, ssrc uint32) error {
	if sm.active.Load() {
		return fmt.Errorf("screenshare already active")
	}

	// Restart pipeline using SDP from previous re-INVITE
	if sm.status == VideoStatusStopped && sm.lastKnownMedia != nil {
		sm.log.Infow("screenshare: restarting pipeline", "sid", sid)
		if _, err := sm.Reconcile(sm.remoteAddr, sm.lastKnownMedia); err != nil {
			return fmt.Errorf("failed to reconcile: %w", err)
		}
		if err := sm.VideoManager.Start(); err != nil {
			return fmt.Errorf("failed to start pipeline: %w", err)
		}
		return sm.processTrackInput(ti, sid, ssrc)
	}

	// Store pending track if pipeline not started
	if sm.status != VideoStatusStarted {
		sm.log.Infow("screenshare: storing pending track", "status", sm.status, "sid", sid)
		sm.pendingTrack = ti
		sm.pendingTrackSID = sid
		return nil
	}

	return sm.processTrackInput(ti, sid, ssrc)
}

// processTrackInput configures the GStreamer pipeline with the track.
func (sm *ScreenshareManager) processTrackInput(ti *TrackInput, sid string, ssrc uint32) error {
	if sm.active.Swap(true) {
		return fmt.Errorf("screenshare already active")
	}

	sm.log.Debugw("screenshare.track_input", "sid", sid, "ssrc", ssrc)

	p, ok := sm.pipeline.(*screenshare_pipeline.ScreensharePipeline)
	if !ok {
		sm.active.Store(false)
		return fmt.Errorf("invalid pipeline type")
	}

	sm.trackInput = ti

	if err := p.SetWebrtcInput(ti.RtpIn); err != nil {
		sm.trackInput = nil
		sm.active.Store(false)
		return fmt.Errorf("failed to set WebRTC input: %w", err)
	}

	if sm.OnScreenshareStarted != nil {
		sm.OnScreenshareStarted()
	}

	return nil
}

func (sm *ScreenshareManager) Stop() error {
	wasActive := sm.active.Swap(false)

	sm.pendingTrack = nil
	sm.pendingTrackSID = ""

	// Close track to unblock pipeline
	if sm.trackInput != nil {
		sm.trackInput.RtpIn.Close()
		sm.trackInput = nil
	}

	if err := sm.VideoManager.stop(); err != nil {
		return err
	}
	sm.floorHeld.Store(false)

	if wasActive && sm.OnScreenshareStopped != nil {
		sm.OnScreenshareStopped()
	}
	return nil
}

func (sm *ScreenshareManager) SetFloorHeld(held bool) {
	sm.floorHeld.Store(held)
	sm.log.Debugw("screenshare.floor_state", "held", held)
}

func (sm *ScreenshareManager) HasFloor() bool {
	return sm.floorHeld.Load()
}

func (sm *ScreenshareManager) IsReady() bool {
	return sm.status >= VideoStatusReady
}

func (sm *ScreenshareManager) IsActive() bool {
	return sm.active.Load()
}

func (sm *ScreenshareManager) SetRemoteAddr(remoteAddr netip.Addr) {
	sm.remoteAddr = remoteAddr
}

func (sm *ScreenshareManager) UpdateRemotePort(port uint16) {
	newRtpDst := netip.AddrPortFrom(sm.remoteAddr, port)
	sm.rtpConn.SetDst(newRtpDst)
	sm.log.Debugw("screenshare.port_updated", "rtpAddr", newRtpDst)
}

func (sm *ScreenshareManager) handleWebrtcTrackEnded() {
	if !sm.active.Load() {
		return
	}

	sm.log.Debugw("screenshare.track_ended")

	sm.pendingTrack = nil
	sm.pendingTrackSID = ""

	if sm.trackInput != nil {
		sm.trackInput.RtpIn.Close()
		sm.trackInput = nil
	}

	if err := sm.VideoManager.stop(); err != nil {
		sm.log.Warnw("failed to stop video manager", err)
	}

	if sm.active.Swap(false) {
		sm.floorHeld.Store(false)
		if sm.OnScreenshareStopped != nil {
			sm.OnScreenshareStopped()
		}
	}
}
