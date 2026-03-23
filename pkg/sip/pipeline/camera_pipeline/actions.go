package camera_pipeline

import (
	"context"
	"fmt"
	"io"
	"net"
	"runtime"
	"runtime/cgo"
	"time"

	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/sip/pkg/sip/pipeline"
	"github.com/livekit/sip/pkg/sip/pipeline/event"
)

func (cp *CameraPipeline) checkReady() error {

	if cp.Pipeline().GetCurrentState() != gst.StatePaused {
		return nil
	}

	checkHandle := func(elem *gst.Element) bool {
		hasHandleVal, err := elem.GetProperty("has-handle")
		if err != nil {
			return false
		}
		hasHandle, ok := hasHandleVal.(bool)
		return ok && hasHandle
	}

	ready := true
	ready = ready && checkHandle(cp.SipRtpIn)
	ready = ready && checkHandle(cp.SipRtpOut)
	ready = ready && checkHandle(cp.SipRtcpIn)
	ready = ready && checkHandle(cp.SipRtcpOut)
	ready = ready && checkHandle(cp.WebrtcRtpOut)
	ready = ready && checkHandle(cp.WebrtcRtcpOut)

	if ready {
		cp.Log().Infow("All handles ready, setting pipeline to PLAYING")
		if err := cp.SetState(gst.StatePlaying); err != nil {
			return fmt.Errorf("failed to set camera pipeline to playing: %w", err)
		}

		for _, e := range []*gst.Element{
			cp.SipRtpIn,
			cp.SipRtpOut,
			cp.SipRtcpIn,
			cp.SipRtcpOut,
			cp.WebrtcRtpOut,
			cp.WebrtcRtcpOut,
		} {
			if !e.SyncStateWithParent() {
				return fmt.Errorf("failed to sync state with parent for element %s", e.GetName())
			}
		}
	}
	return nil
}

func (cp *CameraPipeline) SipIO(rtp, rtcp net.Conn, pt uint8) error {
	rtpHandle := cgo.NewHandle(rtp)
	defer rtpHandle.Delete()
	rtcpHandle := cgo.NewHandle(rtcp)
	defer rtcpHandle.Delete()

	h264Caps := fmt.Sprintf(
		"application/x-rtp,media=video,encoding-name=H264,payload=%d,clock-rate=90000",
		pt)

	cp.Log().Infow("Setting SIP IO",
		"rtp_remote", rtp.RemoteAddr(),
		"rtp_local", rtp.LocalAddr(),
		"rtcp_remote", rtcp.RemoteAddr(),
		"rtcp_local", rtcp.LocalAddr(),
		"caps", h264Caps,
	)

	if _, err := cp.SipRtpBin.Connect("request-pt-map", event.RegisterCallback(context.TODO(), cp.Loop(), func(self *gst.Element, session uint, sipPt uint) *gst.Caps {
		return gst.NewCapsFromString(fmt.Sprintf(
			"application/x-rtp,media=video,encoding-name=H264,payload=%d,clock-rate=90000,rtcp-fb-nack-pli=1,rtcp-fb-ccm-fir=1",
			sipPt))
	})); err != nil {
		return fmt.Errorf("failed to connect to rtpbin request-pt-map signal: %w", err)
	}

	if err := cp.WebrtcToSip.CapsFilter.SetProperty("caps",
		gst.NewCapsFromString(h264Caps+",rtcp-fb-nack-pli=1,rtcp-fb-nack=1,rtcp-fb-ccm-fir=1"),
	); err != nil {
		return fmt.Errorf("failed to set webrtc to sip caps filter caps (pt: %d): %w", pt, err)
	}

	if err := cp.SipRtpIn.SetProperty("caps",
		gst.NewCapsFromString("application/x-rtp,media=video,encoding-name=H264,clock-rate=90000,rtcp-fb-nack-pli=1,rtcp-fb-ccm-fir=1"),
	); err != nil {
		return fmt.Errorf("failed to set sip rtp in caps: %w", err)
	}

	if err := cp.SipRtpIn.SetProperty("handle", uint64(rtpHandle)); err != nil {
		return fmt.Errorf("failed to set rtp in handle: %w", err)
	}

	if err := cp.SipRtpOut.SetProperty("handle", uint64(rtpHandle)); err != nil {
		return fmt.Errorf("failed to set rtp out handle: %w", err)
	}

	if err := cp.SipRtcpIn.SetProperty("handle", uint64(rtcpHandle)); err != nil {
		return fmt.Errorf("failed to set rtcp in handle: %w", err)
	}

	if err := cp.SipRtcpOut.SetProperty("handle", uint64(rtcpHandle)); err != nil {
		return fmt.Errorf("failed to set rtcp out handle: %w", err)
	}

	cp.checkReady()

	return nil
}

func (cp *CameraPipeline) WebrtcOutput(rtp, rtcp io.WriteCloser) error {
	rtpHandle := cgo.NewHandle(rtp)
	defer rtpHandle.Delete()
	rtcpHnd := cgo.NewHandle(rtcp)
	defer rtcpHnd.Delete()

	if err := cp.WebrtcRtpOut.SetProperty("handle", uint64(rtpHandle)); err != nil {
		return fmt.Errorf("failed to set webrtc rtp out handle: %w", err)
	}

	if err := cp.WebrtcRtcpOut.SetProperty("handle", uint64(rtcpHnd)); err != nil {
		return fmt.Errorf("failed to set webrtc rtcp out handle: %w", err)
	}

	cp.checkReady()

	return nil
}

func (cp *CameraPipeline) AddWebrtcTrack(ssrc uint32, rtp, rtcp io.ReadCloser, requestKeyframe func() error) (*WebrtcTrack, error) {
	rtpHandle := cgo.NewHandle(rtp)
	defer rtpHandle.Delete()
	rtcpHnd := cgo.NewHandle(rtcp)
	defer rtcpHnd.Delete()

	cp.Log().Infow("Adding WebRTC track", "ssrc", ssrc)

	track, err := pipeline.AddChain(cp, NewWebrtcTrack(cp.Log(), cp.WebrtcIo, ssrc))
	if err != nil {
		return nil, fmt.Errorf("failed to add webrtc track chain: %w", err)
	}

	track.RequestKeyframe = requestKeyframe
	cp.WebrtcIo.Tracks[ssrc] = track

	if err := track.WebrtcRtpIn.SetProperty("handle", uint64(rtpHandle)); err != nil {
		return nil, fmt.Errorf("failed to set webrtc rtp in handle: %w", err)
	}

	if err := track.WebrtcRtcpIn.SetProperty("handle", uint64(rtcpHnd)); err != nil {
		return nil, fmt.Errorf("failed to set webrtc rtcp in handle: %w", err)
	}

	if err := pipeline.LinkChains(cp, track); err != nil {
		return nil, fmt.Errorf("failed to link webrtc track chain: %w", err)
	}

	return track, nil
}

// DisconnectWebrtcTrack disconnects a track from the pipeline graph (fast).
// Returns the track for deferred Cleanup(), or nil if not found.
func (cp *CameraPipeline) DisconnectWebrtcTrack(ssrc uint32) *WebrtcTrack {
	track, ok := cp.WebrtcIo.Tracks[ssrc]
	if !ok {
		return nil
	}

	activePadName := cp.getActivePadName()
	trackPadName := ""
	if track.SelPad != nil {
		trackPadName = track.SelPad.GetName()
	}
	isActive := activePadName == trackPadName && trackPadName != ""

	cp.pendingSwitchSSRC = 0

	// Switch to an alternate track before disconnecting the active one
	var newTrack *WebrtcTrack
	if isActive {
		for s, t := range cp.WebrtcIo.Tracks {
			if s != ssrc && t.SelPad != nil {
				newTrack = t
				break
			}
		}
	}

	if isActive && newTrack != nil {
		if err := cp.WebrtcIo.InputSelector.SetProperty("active-pad", newTrack.SelPad); err != nil {
			cp.Log().Errorw("failed to switch to alternate track", err, "newSsrc", newTrack.SSRC)
		}

		if err := cp.RequestTrackKeyframe(newTrack); err != nil {
			cp.Log().Warnw("failed to request keyframe from alternate track", err, "newSsrc", newTrack.SSRC)
		}

		cp.ResetX264Encoder()
	}

	track.Disconnect()
	return track
}

// RemoveWebrtcTrack disconnects and immediately cleans up a track (blocking).
func (cp *CameraPipeline) RemoveWebrtcTrack(ssrc uint32) error {
	track := cp.DisconnectWebrtcTrack(ssrc)
	if track == nil {
		return fmt.Errorf("webrtc track with ssrc %d not found", ssrc)
	}
	track.Cleanup()
	return nil
}

func (cp *CameraPipeline) SwitchWebrtcInput(ssrc uint32) error {
	track, ok := cp.WebrtcIo.Tracks[ssrc]
	cp.Log().Infow("SwitchWebrtcInput", "ssrc", ssrc, "trackFound", ok,
		"isActive", cp.isActiveTrack(ssrc), "pendingSwitch", cp.pendingSwitchSSRC,
		"totalTracks", len(cp.WebrtcIo.Tracks))

	if !ok {
		return fmt.Errorf("webrtc track with ssrc %d not found", ssrc)
	}

	if track.SelPad == nil {
		cp.Log().Infow("SwitchWebrtcInput: SelPad nil, deferring until LinkParent", "ssrc", ssrc)
		cp.pendingSwitchSSRC = ssrc
		cp.switchStartTime = time.Now()
		if cp.switchTimer != nil {
			cp.switchTimer.Stop()
		}
		cp.switchTimer = time.AfterFunc(MaxKeyframeWaitTime, func() {
			if cp.pendingSwitchSSRC == ssrc {
				cp.Log().Warnw("keyframe timeout (timer), forcing fallback switch", nil, "ssrc", ssrc)
				cp.executeFallbackSwitch(ssrc)
			}
		})
		return nil
	}

	if cp.isActiveTrack(ssrc) {
		cp.Log().Debugw("SwitchWebrtcInput: already active", "ssrc", ssrc)
		return nil
	}

	if cp.pendingSwitchSSRC == ssrc {
		cp.Log().Debugw("SwitchWebrtcInput: already pending", "ssrc", ssrc)
		return nil
	}

	if cp.pendingSwitchSSRC != 0 {
		cp.Log().Debugw("SwitchWebrtcInput: mid-switch, skipping",
			"pending", cp.pendingSwitchSSRC, "requested", ssrc)
		return nil
	}

	if len(cp.WebrtcIo.Tracks) <= 1 {
		cp.Log().Debugw("SwitchWebrtcInput: only 1 track, no switch needed", "ssrc", ssrc)
		return nil
	}

	cp.pendingSwitchSSRC = ssrc
	cp.switchStartTime = time.Now()
	cp.lastPLITime = time.Now()

	if err := cp.RequestTrackKeyframe(track); err != nil {
		cp.Log().Warnw("failed to request keyframe", err, "ssrc", ssrc)
	}

	// Timer fallback: force fallback switch if no keyframe arrives via pad probes
	if cp.switchTimer != nil {
		cp.switchTimer.Stop()
	}
	cp.switchTimer = time.AfterFunc(MaxKeyframeWaitTime, func() {
		if cp.pendingSwitchSSRC == ssrc {
			cp.Log().Warnw("keyframe timeout (timer), forcing fallback switch", nil, "ssrc", ssrc)
			cp.executeFallbackSwitch(ssrc)
		}
	})

	return nil
}

func (cp *CameraPipeline) onTrackKeyframe(ssrc uint32) {
	if cp.pendingSwitchSSRC == 0 || cp.pendingSwitchSSRC != ssrc {
		return
	}

	if err := cp.executeSwitch(ssrc); err != nil {
		cp.Log().Errorw("switch execution failed", err, "ssrc", ssrc)
	}
}

func (cp *CameraPipeline) checkPLIRetry(ssrc uint32) {
	if cp.pendingSwitchSSRC != ssrc {
		return
	}

	// Dirty switch after MaxKeyframeWaitTime
	if time.Since(cp.switchStartTime) >= MaxKeyframeWaitTime {
		cp.Log().Warnw("keyframe timeout, forcing fallback switch", nil,
			"ssrc", ssrc, "waited", time.Since(cp.switchStartTime))
		cp.executeFallbackSwitch(ssrc)
		return
	}

	if time.Since(cp.lastPLITime) >= PLIRetryInterval {
		if track, ok := cp.WebrtcIo.Tracks[ssrc]; ok {
			cp.lastPLITime = time.Now()
			if err := cp.RequestTrackKeyframe(track); err != nil {
				cp.Log().Warnw("failed to retry PLI", err, "ssrc", ssrc)
			}
		}
	}
}

func (cp *CameraPipeline) executeSwitch(ssrc uint32) error {
	if cp.switchTimer != nil {
		cp.switchTimer.Stop()
		cp.switchTimer = nil
	}
	track, ok := cp.WebrtcIo.Tracks[ssrc]
	if !ok {
		return fmt.Errorf("track %d not found", ssrc)
	}

	if track.SelPad == nil {
		return fmt.Errorf("track %d has no selector pad", ssrc)
	}

	track.SeenKeyframeInQueue = false

	if err := cp.WebrtcIo.InputSelector.SetProperty("active-pad", track.SelPad); err != nil {
		return fmt.Errorf("failed to set active-pad: %w", err)
	}

	cp.ResetVp8Decoder()
	cp.ResetX264Encoder()
	cp.pendingSwitchSSRC = 0

	return nil
}

// executeFallbackSwitch switches the active pad and allows frames through without a keyframe.
func (cp *CameraPipeline) executeFallbackSwitch(ssrc uint32) {
	if cp.switchTimer != nil {
		cp.switchTimer.Stop()
		cp.switchTimer = nil
	}
	track, ok := cp.WebrtcIo.Tracks[ssrc]
	if !ok || track.SelPad == nil {
		cp.pendingSwitchSSRC = 0
		return
	}
	track.SeenKeyframeInQueue = true
	if err := cp.WebrtcIo.InputSelector.SetProperty("active-pad", track.SelPad); err != nil {
		cp.Log().Errorw("failed fallback switch", err, "ssrc", ssrc)
	}
	if err := cp.RequestTrackKeyframe(track); err != nil {
		cp.Log().Warnw("failed to request keyframe after fallback switch", err, "ssrc", ssrc)
	}
	cp.ResetX264Encoder()
	cp.pendingSwitchSSRC = 0
}

// isActiveTrack checks if the given SSRC is currently the active track.
func (cp *CameraPipeline) isActiveTrack(ssrc uint32) bool {
	track, ok := cp.WebrtcIo.Tracks[ssrc]
	if !ok || track.SelPad == nil {
		return false
	}
	return cp.getActivePadName() == track.SelPad.GetName()
}

// getActivePadName returns the name of the currently active InputSelector pad.
func (cp *CameraPipeline) getActivePadName() string {
	active, err := cp.WebrtcIo.InputSelector.GetProperty("active-pad")
	if err != nil || active == nil {
		return "none"
	}
	if pad, ok := active.(*gst.Pad); ok && pad != nil {
		return pad.GetName()
	}
	return "unknown"
}

// FallbackSwitchWebrtcInput performs an immediate switch without waiting for keyframe (deprecated).
func (cp *CameraPipeline) FallbackSwitchWebrtcInput(ssrc uint32) error {
	track, ok := cp.WebrtcIo.Tracks[ssrc]
	if !ok {
		return fmt.Errorf("webrtc track with ssrc %d not found", ssrc)
	}

	if track.SelPad == nil {
		return fmt.Errorf("webrtc track with ssrc %d has no sel pad", ssrc)
	}

	targetPadName := track.SelPad.GetName()

	active, err := cp.WebrtcIo.InputSelector.GetProperty("active-pad")
	if err == nil && active != nil {
		activePad, ok := active.(*gst.Pad)
		if ok && activePad != nil {
			activePadName := activePad.GetName()
			if activePadName != "" && activePadName == targetPadName {
				cp.Log().Infow("webrtc input already set to desired ssrc", "ssrc", ssrc)
				return nil
			}
		}
	}

	cp.Log().Infow("switching webrtc input to new ssrc", "ssrc", ssrc)

	if err := cp.WebrtcIo.InputSelector.SetProperty("active-pad", track.SelPad); err != nil {
		return fmt.Errorf("failed to switch webrtc input to ssrc %d: %w", ssrc, err)
	}

	cp.Log().Infow("switched webrtc input to new ssrc", "ssrc", ssrc)

	if err := cp.ForceKeyframeOnEncoder(); err != nil {
		cp.Log().Warnw("failed to force keyframe on encoder after switch", err, "ssrc", ssrc)
	}

	return nil
}

func (cp *CameraPipeline) RequestTrackKeyframe(wt *WebrtcTrack) error {
	if wt.RequestKeyframe != nil {
		if err := wt.RequestKeyframe(); err != nil {
			cp.Log().Warnw("failed to send PLI", err, "ssrc", wt.SSRC)
			return err
		}
	}
	return nil
}

// RequestSipKeyframe requests a keyframe from the SIP device via RTCP PLI.
// Safe to call from any goroutine.
func (cp *CameraPipeline) RequestSipKeyframe() {
	// Non-blocking send to channel - coalesces multiple rapid requests
	select {
	case cp.sipKeyframeRequests <- struct{}{}:
	default:
		// Channel full, request already pending
	}
}

func (cp *CameraPipeline) doRequestSipKeyframe() {
	cp.Log().Infow("Requesting keyframe from SIP device")

	sinkPad := cp.SipToWebrtc.H264Depay.GetStaticPad("sink")
	if sinkPad == nil {
		cp.Log().Warnw("H264Depay sink pad not found for PLI request", nil)
		return
	}

	peerPad := sinkPad.GetPeer()
	if peerPad == nil {
		cp.Log().Warnw("H264Depay sink pad has no peer for PLI request", nil)
		return
	}

	fkuStruct := gst.NewStructure("GstForceKeyUnit")
	runtime.SetFinalizer(fkuStruct, nil)
	fkuStruct.SetValue("all-headers", true)

	fkuEvent := gst.NewCustomEvent(gst.EventTypeCustomUpstream, fkuStruct)

	if !peerPad.SendEvent(fkuEvent) {
		cp.Log().Warnw("Failed to send GstForceKeyUnit event", nil)
	} else {
		cp.Log().Infow("Sent GstForceKeyUnit event to SIP rtpbin")
	}
}

// ForceKeyframeOnEncoder sends an upstream ForceKeyUnit event to x264enc.
// Uses upstream event on src pad to avoid blocking on a backed-up encoder.
func (cp *CameraPipeline) ForceKeyframeOnEncoder() error {
	fkuStruct := gst.NewStructure("GstForceKeyUnit")
	runtime.SetFinalizer(fkuStruct, nil)
	fkuStruct.SetValue("running-time", gst.ClockTimeNone)
	fkuStruct.SetValue("all-headers", true)
	fkuStruct.SetValue("count", uint(0))

	fkuEvent := gst.NewCustomEvent(gst.EventTypeCustomUpstream, fkuStruct)

	srcPad := cp.WebrtcToSip.X264Enc.GetStaticPad("src")
	if srcPad == nil {
		return fmt.Errorf("x264enc src pad not found")
	}

	srcPad.SendEvent(fkuEvent)
	return nil
}

func (cp *CameraPipeline) ResetVp8Decoder() {
}

func (cp *CameraPipeline) ResetX264Encoder() {
	cp.ForceKeyframeOnEncoder()
}

func (cp *CameraPipeline) FlushVp8Decoder() error {
	sinkPad := cp.WebrtcToSip.Vp8Dec.GetStaticPad("sink")
	if sinkPad == nil {
		return fmt.Errorf("vp8dec sink pad not found")
	}

	flushStart := gst.NewFlushStartEvent()
	sinkPad.SendEvent(flushStart)

	flushStop := gst.NewFlushStopEvent(true)
	sinkPad.SendEvent(flushStop)

	return nil
}
