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
			"application/x-rtp,media=video,encoding-name=H264,payload=%d,clock-rate=90000",
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
		gst.NewCapsFromString(h264Caps+",rtcp-fb-nack-pli=1,rtcp-fb-nack=1,rtcp-fb-ccm-fir=1"),
	); err != nil {
		return fmt.Errorf("failed to set sip rtp in caps (pt: %d): %w", pt, err)
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

func (cp *CameraPipeline) RemoveWebrtcTrack(ssrc uint32) error {
	track, ok := cp.WebrtcIo.Tracks[ssrc]
	if !ok {
		return fmt.Errorf("webrtc track with ssrc %d not found", ssrc)
	}

	// Check if this is the currently active track
	activePadName := cp.getActivePadName()
	trackPadName := ""
	if track.SelPad != nil {
		trackPadName = track.SelPad.GetName()
	}
	isActive := activePadName == trackPadName && trackPadName != ""

	cp.Log().Infow("[SWITCH_DEBUG] Removing track", "ssrc", ssrc,
		"isActive", isActive, "activePad", activePadName, "trackPad", trackPadName)

	if cp.pendingSwitchSSRC == ssrc {
		cp.Log().Infow("[SWITCH_DEBUG] Clearing pending switch for removed track", "ssrc", ssrc)
		cp.pendingSwitchSSRC = 0
	}

	// Find another track to switch to if removing the active one
	var newTrack *WebrtcTrack
	if isActive {
		for s, t := range cp.WebrtcIo.Tracks {
			if s != ssrc && t.SelPad != nil {
				newTrack = t
				break
			}
		}
	}

	if isActive && newTrack == nil {
		cp.Log().Warnw("[SWITCH_DEBUG] Active track leaving but no alternate track found!",
			nil, "ssrc", ssrc, "totalTracks", len(cp.WebrtcIo.Tracks))
	}
	if isActive && newTrack != nil {
		cp.Log().Infow("[SWITCH_DEBUG] Active track leaving, switching to alternate",
			"oldSsrc", ssrc, "newSsrc", newTrack.SSRC)

		// Switch immediately but drop P-frames until keyframe arrives
		newTrack.SeenKeyframeInQueue = false
		cp.pendingSwitchSSRC = newTrack.SSRC
		cp.switchStartTime = time.Now()
		cp.lastPLITime = time.Now()

		if err := cp.WebrtcIo.InputSelector.SetProperty("active-pad", newTrack.SelPad); err != nil {
			cp.Log().Errorw("failed to switch to alternate track", err, "newSsrc", newTrack.SSRC)
		}

		if err := cp.RequestTrackKeyframe(newTrack); err != nil {
			cp.Log().Warnw("failed to request keyframe from alternate track", err, "newSsrc", newTrack.SSRC)
		}

		cp.ResetX264Encoder()
	}

	if err := track.Close(); err != nil {
		return fmt.Errorf("failed to close webrtc track with ssrc %d: %w", ssrc, err)
	}

	return nil
}

// SwitchWebrtcInput performs a keyframe-aware switch to a new track.
func (cp *CameraPipeline) SwitchWebrtcInput(ssrc uint32) error {
	cp.Log().Infow("[SWITCH_DEBUG] === SWITCH REQUESTED ===", "targetSSRC", ssrc)

	track, ok := cp.WebrtcIo.Tracks[ssrc]
	if !ok {
		cp.Log().Errorw("[SWITCH_DEBUG] Track not found", nil, "ssrc", ssrc)
		return fmt.Errorf("webrtc track with ssrc %d not found", ssrc)
	}

	if track.SelPad == nil {
		cp.Log().Debugw("[SWITCH_DEBUG] Track not fully set up yet", "ssrc", ssrc)
		return nil
	}

	if cp.isActiveTrack(ssrc) {
		cp.Log().Infow("[SWITCH_DEBUG] Already on target track", "ssrc", ssrc)
		return nil
	}

	if cp.pendingSwitchSSRC == ssrc {
		cp.Log().Infow("[SWITCH_DEBUG] Switch already pending for this target", "ssrc", ssrc)
		return nil
	}

	cp.Log().Infow("[SWITCH_DEBUG] Requesting keyframe, will switch on arrival",
		"ssrc", ssrc, "hasEverReceivedKeyframe", track.HasKeyframe)
	cp.pendingSwitchSSRC = ssrc
	cp.switchStartTime = time.Now()
	cp.lastPLITime = time.Now()

	if err := cp.RequestTrackKeyframe(track); err != nil {
		cp.Log().Warnw("[SWITCH_DEBUG] Failed to request keyframe", err, "ssrc", ssrc)
	}

	return nil
}

// onTrackKeyframe completes a pending switch when keyframe arrives from target track.
func (cp *CameraPipeline) onTrackKeyframe(ssrc uint32) {
	if cp.pendingSwitchSSRC == 0 || cp.pendingSwitchSSRC != ssrc {
		return
	}

	cp.Log().Infow("[SWITCH_DEBUG] Keyframe arrived for pending switch",
		"ssrc", ssrc,
		"waitDuration", time.Since(cp.switchStartTime))

	if err := cp.executeSwitch(ssrc); err != nil {
		cp.Log().Errorw("[SWITCH_DEBUG] Switch execution failed", err, "ssrc", ssrc)
	}
}

// checkPLIRetry retries PLI requests while waiting for keyframe.
func (cp *CameraPipeline) checkPLIRetry(ssrc uint32) {
	if cp.pendingSwitchSSRC != ssrc {
		return
	}

	elapsed := time.Since(cp.switchStartTime)

	if time.Since(cp.lastPLITime) >= PLIRetryInterval {
		if track, ok := cp.WebrtcIo.Tracks[ssrc]; ok {
			cp.Log().Infow("[SWITCH_DEBUG] Retrying PLI request",
				"ssrc", ssrc, "elapsed", elapsed)
			cp.lastPLITime = time.Now()
			if err := cp.RequestTrackKeyframe(track); err != nil {
				cp.Log().Warnw("[SWITCH_DEBUG] Failed to retry PLI", err, "ssrc", ssrc)
			}
		}
	}

	if elapsed > MaxKeyframeWaitTime && int(elapsed/time.Second) != int((elapsed-PLIRetryInterval)/time.Second) {
		cp.Log().Warnw("[SWITCH_DEBUG] Still waiting for keyframe", nil,
			"ssrc", ssrc, "waitDuration", elapsed)
	}
}

// executeSwitch performs the actual InputSelector switch and resets decoder/encoder state.
func (cp *CameraPipeline) executeSwitch(ssrc uint32) error {
	track, ok := cp.WebrtcIo.Tracks[ssrc]
	if !ok {
		return fmt.Errorf("track %d not found", ssrc)
	}

	if track.SelPad == nil {
		return fmt.Errorf("track %d has no selector pad", ssrc)
	}

	oldPad := cp.getActivePadName()
	cp.Log().Infow("[SWITCH_DEBUG] Executing switch",
		"ssrc", ssrc, "oldPad", oldPad, "newPad", track.SelPad.GetName())

	track.SeenKeyframeInQueue = false

	if err := cp.WebrtcIo.InputSelector.SetProperty("active-pad", track.SelPad); err != nil {
		return fmt.Errorf("failed to set active-pad: %w", err)
	}

	cp.ResetVp8Decoder()
	cp.ResetX264Encoder()
	cp.pendingSwitchSSRC = 0

	cp.Log().Infow("[SWITCH_DEBUG] === SWITCH COMPLETE ===", "ssrc", ssrc)
	return nil
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

// DirtySwitchWebrtcInput performs an immediate switch without waiting for keyframe (deprecated).
func (cp *CameraPipeline) DirtySwitchWebrtcInput(ssrc uint32) error {
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

// RequestTrackKeyframe sends RTCP PLI to request keyframe from WebRTC sender.
func (cp *CameraPipeline) RequestTrackKeyframe(wt *WebrtcTrack) error {
	cp.Log().Infow("[SWITCH_DEBUG] Requesting keyframe via RTCP PLI", "ssrc", wt.SSRC)

	if wt.RequestKeyframe != nil {
		if err := wt.RequestKeyframe(); err != nil {
			cp.Log().Warnw("[SWITCH_DEBUG] Failed to send PLI", err, "ssrc", wt.SSRC)
			return err
		}
	} else {
		cp.Log().Warnw("[SWITCH_DEBUG] No keyframe request callback available", nil, "ssrc", wt.SSRC)
	}

	return nil
}

// ForceKeyframeOnEncoder sends GstForceKeyUnit event to x264enc.
func (cp *CameraPipeline) ForceKeyframeOnEncoder() error {
	cp.Log().Infow("[SWITCH_DEBUG] Forcing keyframe on x264enc encoder")

	fkuStruct := gst.NewStructure("GstForceKeyUnit")
	runtime.SetFinalizer(fkuStruct, nil)
	fkuStruct.SetValue("running-time", gst.ClockTimeNone)
	fkuStruct.SetValue("all-headers", true)
	fkuStruct.SetValue("count", uint(0))

	fkuEvent := gst.NewCustomEvent(gst.EventTypeCustomDownstream, fkuStruct)

	sinkPad := cp.WebrtcToSip.X264Enc.GetStaticPad("sink")
	if sinkPad == nil {
		return fmt.Errorf("x264enc sink pad not found")
	}

	if !sinkPad.SendEvent(fkuEvent) {
		cp.Log().Warnw("[SWITCH_DEBUG] Failed to send GstForceKeyUnit event", nil)
	}

	return nil
}

// ResetVp8Decoder resets the VP8 decoder state for track switching.
func (cp *CameraPipeline) ResetVp8Decoder() {
	cp.Log().Infow("[SWITCH_DEBUG] Resetting VP8 decoder for track switch")
}

// ResetX264Encoder resets the x264 encoder state and forces an I-frame.
func (cp *CameraPipeline) ResetX264Encoder() {
	cp.Log().Infow("[SWITCH_DEBUG] Resetting x264 encoder for track switch")
	if err := cp.ForceKeyframeOnEncoder(); err != nil {
		cp.Log().Warnw("[SWITCH_DEBUG] Failed to force encoder keyframe", err)
	}
}

// FlushVp8Decoder sends flush events to VP8 decoder (may cause pipeline stalls).
func (cp *CameraPipeline) FlushVp8Decoder() error {
	cp.Log().Infow("[SWITCH_DEBUG] Flushing VP8 decoder")

	sinkPad := cp.WebrtcToSip.Vp8Dec.GetStaticPad("sink")
	if sinkPad == nil {
		return fmt.Errorf("vp8dec sink pad not found")
	}

	flushStart := gst.NewFlushStartEvent()
	if !sinkPad.SendEvent(flushStart) {
		cp.Log().Warnw("[SWITCH_DEBUG] Failed to send flush-start event", nil)
	}

	flushStop := gst.NewFlushStopEvent(true)
	if !sinkPad.SendEvent(flushStop) {
		cp.Log().Warnw("[SWITCH_DEBUG] Failed to send flush-stop event", nil)
	}

	return nil
}
