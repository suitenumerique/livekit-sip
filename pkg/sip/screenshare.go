package sip

import (
	"fmt"
	"net/netip"
	"sync/atomic"

	sdpv2 "github.com/livekit/media-sdk/sdp/v2"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/sip/pkg/sip/pipeline"
	"github.com/livekit/sip/pkg/sip/pipeline/screenshare_pipeline"
)

type ScreenshareManager struct {
	*VideoManager
	active    atomic.Bool
	floorHeld atomic.Bool // BFCP floor is held by remote SIP participant

	// Remote address for UpdateRemotePort after re-INVITE
	remoteAddr netip.Addr

	// Callbacks for screenshare lifecycle events
	OnScreenshareStarted func() // Called when WebRTC screenshare track starts
	OnScreenshareStopped func() // Called when WebRTC screenshare track stops
}

func NewScreenshareManager(log logger.Logger, room *Room, opts *MediaOptions) (*ScreenshareManager, error) {
	sm := &ScreenshareManager{}

	vm, err := NewVideoManager(log, room, opts, sm)
	if err != nil {
		return nil, err
	}
	sm.VideoManager = vm

	return sm, nil
}

func (sm *ScreenshareManager) NewPipeline(media *sdpv2.SDPMedia) (pipeline.GspPipeline, error) {
	sm.log.Debugw("screenshare.pipeline.create", "codec", media.Codec.Name, "pt", media.Codec.PayloadType)
	p, err := screenshare_pipeline.New(sm.log, media.Codec.PayloadType)
	if err != nil {
		return nil, fmt.Errorf("failed to create screenshare pipeline: %w", err)
	}
	// p.Monitor()

	// WebRTC -> SIP pipeline
	webrtcRtpIn, err := NewGstWriter(p.WebrtcToSip.WebrtcRtpAppSrc)
	if err != nil {
		return nil, fmt.Errorf("failed to create WebRTC RTP input writer: %w", err)
	}
	go func() {
		sm.Copy(webrtcRtpIn, sm.sipRtpIn)
		sm.handleWebrtcTrackEnded()
	}()

	sipRtpOut, err := NewGstReader(p.WebrtcToSip.SipRtpAppSink)
	if err != nil {
		return nil, fmt.Errorf("failed to create SIP RTP output reader: %w", err)
	}
	go sm.Copy(sm.sipRtpOut, sipRtpOut)

	return p, nil
}

func (sm *ScreenshareManager) WebrtcTrackInput(ti *TrackInput, sid string, ssrc uint32) {
	if sm.active.Swap(true) {
		sm.log.Warnw("screenshare already active", nil)
		return
	}

	// Capture callback before lock to avoid deadlock with NeedsReInvite()
	var notifyStarted func()

	sm.mu.Lock()
	if sm.status != VideoStatusStarted {
		sm.log.Warnw("video manager not started", nil, "status", sm.status)
		sm.mu.Unlock()
		return
	}

	sm.log.Debugw("screenshare.webrtc.track_input", "sid", sid, "ssrc", ssrc)

	if r := sm.sipRtpIn.Swap(ti.RtpIn); r != nil {
		_ = r.Close()
	}

	notifyStarted = sm.OnScreenshareStarted
	sm.mu.Unlock()

	if notifyStarted != nil {
		notifyStarted()
	}
}

func (sm *ScreenshareManager) Stop() error {
	wasActive := sm.active.Swap(false)

	if err := sm.VideoManager.Stop(); err != nil {
		return err
	}
	sm.floorHeld.Store(false)

	// Notify that screenshare has stopped (triggers BFCP floor release)
	if wasActive && sm.OnScreenshareStopped != nil {
		sm.OnScreenshareStopped()
	}
	return nil
}

// SetFloorHeld updates the BFCP floor state for screenshare.
func (sm *ScreenshareManager) SetFloorHeld(held bool) {
	sm.floorHeld.Store(held)
	sm.log.Debugw("screenshare.floor_state", "held", held)
}

// HasFloor returns true if the remote SIP participant holds the floor.
func (sm *ScreenshareManager) HasFloor() bool {
	return sm.floorHeld.Load()
}

// IsReady returns true if the screenshare manager is set up and ready to receive tracks.
func (sm *ScreenshareManager) IsReady() bool {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	return sm.status >= VideoStatusReady
}

// SetRemoteAddr stores the remote address for UpdateRemotePort after re-INVITE.
func (sm *ScreenshareManager) SetRemoteAddr(remoteAddr netip.Addr) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.remoteAddr = remoteAddr
}

// UpdateRemotePort updates the RTP/RTCP destination after re-INVITE success.
func (sm *ScreenshareManager) UpdateRemotePort(port uint16) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	newRtpDst := netip.AddrPortFrom(sm.remoteAddr, port)
	newRtcpDst := netip.AddrPortFrom(sm.remoteAddr, port+1)

	sm.rtpConn.SetDst(newRtpDst)
	sm.rtcpConn.SetDst(newRtcpDst)

	sm.log.Debugw("screenshare.remote_port_updated", "rtpAddr", newRtpDst)
}

// handleWebrtcTrackEnded is called when the WebRTC->SIP Copy loop terminates.
// Stops VideoManager to reset status, allowing next track to trigger re-INVITE.
func (sm *ScreenshareManager) handleWebrtcTrackEnded() {
	if !sm.active.Load() {
		return
	}

	sm.log.Debugw("screenshare.webrtc.track_ended")

	if err := sm.VideoManager.Stop(); err != nil {
		sm.log.Warnw("failed to stop video manager", err)
	}

	if sm.active.Swap(false) {
		sm.floorHeld.Store(false)
		if sm.OnScreenshareStopped != nil {
			sm.OnScreenshareStopped()
		}
	}
}
