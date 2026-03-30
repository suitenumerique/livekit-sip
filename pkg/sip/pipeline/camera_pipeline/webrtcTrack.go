package camera_pipeline

import (
	"fmt"
	"sync/atomic"

	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/sip/pkg/sip/pipeline"
)

func NewWebrtcTrack(log logger.Logger, parent *WebrtcIo, ssrc uint32) *WebrtcTrack {
	return &WebrtcTrack{
		log:    log.WithComponent("webrtc_track").WithValues("ssrc", ssrc),
		parent: parent,
		SSRC:   ssrc,
	}
}

type WebrtcTrack struct {
	log    logger.Logger
	parent *WebrtcIo

	SSRC uint32

	WebrtcRtpIn  *gst.Element
	Vp8Depay     *gst.Element
	RtpQueue     *gst.Element
	WebrtcRtcpIn *gst.Element

	RtpPad        *gst.Pad
	RtcpPad       *gst.Pad
	RtpBinPad     *gst.Pad
	RtpFunnelPad  *gst.Pad
	RtcpFunnelPad *gst.Pad
	SelPad        *gst.Pad

	HasKeyframe         bool
	SeenKeyframeInQueue atomic.Bool
	RequestKeyframe     func() error
	SetSubscribed       func(bool) error
}

var _ pipeline.GstChain = (*WebrtcTrack)(nil)

// Create implements GstChain.
func (wt *WebrtcTrack) Create() error {
	var err error

	wt.WebrtcRtpIn, err = gst.NewElementWithProperties("sourcereader", map[string]interface{}{
		"name":         fmt.Sprintf("webrtc_rtp_in_%d", wt.SSRC),
		"caps":         gst.NewCapsFromString(VP8CAPS),
		"do-timestamp": true,
	})
	if err != nil {
		return fmt.Errorf("failed to create webrtc rtp sourcereader: %w", err)
	}

	wt.Vp8Depay, err = gst.NewElementWithProperties("rtpvp8depay", map[string]interface{}{
		"request-keyframe": true,
	})
	if err != nil {
		return fmt.Errorf("failed to create webrtc vp8 depayloader: %w", err)
	}

	wt.RtpQueue, err = gst.NewElementWithProperties("queue", map[string]interface{}{})
	if err != nil {
		return fmt.Errorf("failed to create webrtc rtp queue: %w", err)
	}

	wt.WebrtcRtcpIn, err = gst.NewElementWithProperties("sourcereader", map[string]interface{}{
		"name":         fmt.Sprintf("webrtc_rtcp_in_%d", wt.SSRC),
		"caps":         gst.NewCapsFromString("application/x-rtcp"),
		"do-timestamp": true,
	})
	if err != nil {
		return fmt.Errorf("failed to create webrtc rtcp appsrc: %w", err)
	}

	return nil
}

func (wt *WebrtcTrack) Add() error {
	if err := wt.parent.pipeline.Pipeline().AddMany(
		wt.WebrtcRtpIn,
		wt.Vp8Depay,
		wt.RtpQueue,
		wt.WebrtcRtcpIn,
	); err != nil {
		return fmt.Errorf("failed to add webrtc track elements to pipeline: %w", err)
	}
	return nil
}

func (wt *WebrtcTrack) Link() error {
	wt.RtpPad = wt.WebrtcRtpIn.GetStaticPad("src")
	wt.RtcpPad = wt.WebrtcRtcpIn.GetStaticPad("src")

	wt.RtpFunnelPad = wt.parent.RtpFunnel.GetRequestPad("sink_%u")
	if err := pipeline.LinkPad(wt.RtpPad, wt.RtpFunnelPad); err != nil {
		return fmt.Errorf("failed to link webrtc rtp to funnel: %w", err)
	}

	wt.RtcpPad.AddProbe(gst.PadProbeTypeBuffer, NewRtcpSsrcFilter(wt.SSRC))
	wt.RtcpFunnelPad = wt.parent.RtcpFunnel.GetRequestPad("sink_%u")
	if err := pipeline.LinkPad(wt.RtcpPad, wt.RtcpFunnelPad); err != nil {
		return fmt.Errorf("failed to link webrtc rtcp to funnel: %w", err)
	}

	return pipeline.SyncElements(
		wt.WebrtcRtpIn,
		wt.WebrtcRtcpIn,
	)
}

func (wt *WebrtcTrack) LinkParent(rtpbinPad *gst.Pad) error {
	wt.RtpBinPad = rtpbinPad
	if err := pipeline.LinkPad(
		wt.RtpBinPad,
		wt.Vp8Depay.GetStaticPad("sink"),
	); err != nil {
		return fmt.Errorf("failed to link webrtc rtpbin pad to depayloader: %w", err)
	}

	if err := gst.ElementLinkMany(
		wt.Vp8Depay,
		wt.RtpQueue,
	); err != nil {
		return fmt.Errorf("failed to link webrtc track rtp elements: %w", err)
	}

	wt.SelPad = wt.parent.InputSelector.GetRequestPad("sink_%u")
	if err := pipeline.LinkPad(wt.RtpQueue.GetStaticPad("src"), wt.SelPad); err != nil {
		return fmt.Errorf("failed to link webrtc rtp queue to input selector: %w", err)
	}

	// Install keyframe probe before queue and P-frame drop probe after queue
	depayOutPad := wt.Vp8Depay.GetStaticPad("src")
	depayOutPad.AddProbe(gst.PadProbeTypeBuffer, wt.keyframeProbe)

	queueOutPad := wt.RtpQueue.GetStaticPad("src")
	queueOutPad.AddProbe(gst.PadProbeTypeBuffer, wt.queueOutputProbe)

	// Check if this track has a pending switch waiting for LinkParent
	isPendingSwitch := wt.parent.pipeline.pendingSwitchSSRC.Load() == wt.SSRC
	if isPendingSwitch {
		wt.log.Infow("pending switch: clearing timer before SyncElements", "ssrc", wt.SSRC)
		if wt.parent.pipeline.switchTimer != nil {
			wt.parent.pipeline.switchTimer.Stop()
			wt.parent.pipeline.switchTimer = nil
		}
	}

	// Start data flow
	if err := pipeline.SyncElements(wt.Vp8Depay, wt.RtpQueue); err != nil {
		return fmt.Errorf("failed to sync webrtc track elements: %w", err)
	}

	if isPendingSwitch {
		// Switch immediately — brief artifacts are better than a multi-second freeze
		wt.log.Infow("pending switch: executing immediate switch", "ssrc", wt.SSRC)
		wt.parent.pipeline.executeFallbackSwitch(wt.SSRC)
	} else if len(wt.parent.Tracks) <= 1 {
		// First track — auto-switch
		if err := wt.parent.pipeline.SwitchWebrtcInput(wt.SSRC); err != nil {
			return fmt.Errorf("failed to switch webrtc input to ssrc %d: %w", wt.SSRC, err)
		}
	}

	return nil
}

// keyframeProbe detects keyframes and triggers switch, drops P-frames while waiting.
func (wt *WebrtcTrack) keyframeProbe(pad *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
	buffer := info.GetBuffer()
	if buffer == nil {
		return gst.PadProbeOK
	}

	pendingSSRC := wt.parent.pipeline.pendingSwitchSSRC.Load()
	isKeyframe := !buffer.HasFlags(gst.BufferFlagDeltaUnit)

	if isKeyframe {
		wt.HasKeyframe = true

		if pendingSSRC == wt.SSRC {
			buffer.SetFlags(gst.BufferFlagDiscont)
			// Switch InputSelector inline before the keyframe enters the queue.
			wt.SeenKeyframeInQueue.Store(false)
			wt.parent.InputSelector.SetProperty("active-pad", wt.SelPad)
			wt.parent.pipeline.activeSSRC.Store(wt.SSRC)
			wt.parent.pipeline.needsEncoderReset.Store(true)
			// Dispatch subscribe and timer cleanup to the EventLoop.
			select {
			case wt.parent.pipeline.switchRequests <- wt.SSRC:
			default:
			}
		}
	} else if pendingSSRC == wt.SSRC {
		select {
		case wt.parent.pipeline.pliRetryRequests <- wt.SSRC:
		default:
		}
		return gst.PadProbeDrop
	} else if pendingSSRC != 0 {
		// Check pending switch timeout from other tracks' probes
		select {
		case wt.parent.pipeline.pliRetryRequests <- pendingSSRC:
		default:
		}
	}

	return gst.PadProbeOK
}

func (wt *WebrtcTrack) queueOutputProbe(pad *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
	buffer := info.GetBuffer()
	if buffer == nil {
		return gst.PadProbeOK
	}

	if wt.parent.pipeline.activeSSRC.Load() != wt.SSRC {
		return gst.PadProbeOK
	}

	isKeyframe := !buffer.HasFlags(gst.BufferFlagDeltaUnit)

	if isKeyframe {
		// Reset x264enc on the first keyframe after a track switch.
		if wt.parent.pipeline.needsEncoderReset.CompareAndSwap(true, false) {
			wt.parent.pipeline.ResetX264Encoder()
		}
		wt.SeenKeyframeInQueue.Store(true)

		if wt.parent.pipeline.pendingSwitchSSRC.Load() == wt.SSRC {
			wt.parent.pipeline.pendingSwitchSSRC.Store(0)
		}

		return gst.PadProbeOK
	}

	if !wt.SeenKeyframeInQueue.Load() {
		return gst.PadProbeDrop
	}

	return gst.PadProbeOK
}

// Disconnect releases pads and removes track from parent map.
// Elements are orphaned but still in pipeline — call Cleanup() to finalize.
func (wt *WebrtcTrack) Disconnect() {
	if wt.SelPad != nil {
		active, err := wt.parent.InputSelector.GetProperty("active-pad")
		if err == nil && active != nil {
			if activePad, ok := active.(*gst.Pad); ok && activePad != nil {
				if activePad.GetName() == wt.SelPad.GetName() {
					wt.log.Warnw("Disconnecting active track", nil, "ssrc", wt.SSRC)
				}
			}
		}
	}

	if wt.SelPad != nil {
		wt.parent.InputSelector.ReleaseRequestPad(wt.SelPad)
		wt.SelPad = nil
	}
	if wt.RtpFunnelPad != nil {
		wt.parent.RtpFunnel.ReleaseRequestPad(wt.RtpFunnelPad)
		wt.RtpFunnelPad = nil
	}
	if wt.RtcpFunnelPad != nil {
		wt.parent.RtcpFunnel.ReleaseRequestPad(wt.RtcpFunnelPad)
		wt.RtcpFunnelPad = nil
	}

	delete(wt.parent.Tracks, wt.SSRC)
	wt.log.Infow("Disconnected webrtc track", "ssrc", wt.SSRC)
}

// Cleanup sets orphaned elements to null state and removes them from pipeline.
// Must run on the same OS-locked thread as all GStreamer operations.
func (wt *WebrtcTrack) Cleanup() {
	elements := []*gst.Element{
		wt.WebrtcRtpIn,
		wt.Vp8Depay,
		wt.RtpQueue,
		wt.WebrtcRtcpIn,
	}

	for _, elem := range elements {
		if err := elem.SetState(gst.StateNull); err != nil {
			wt.log.Errorw("Failed to set webrtc track element to null state", err, "element", elem.GetName())
		}
	}

	wt.parent.pipeline.Pipeline().RemoveMany(elements...)
	wt.log.Infow("Cleaned up webrtc track elements", "ssrc", wt.SSRC)
}

// Close disconnects and immediately cleans up (blocking).
// Prefer Disconnect() + deferred Cleanup() for non-blocking removal.
func (wt *WebrtcTrack) Close() error {
	wt.Disconnect()
	wt.Cleanup()
	return nil
}
