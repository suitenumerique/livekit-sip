package camera_pipeline

import (
	"fmt"

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
	SeenKeyframeInQueue bool
	RequestKeyframe     func() error
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

	if err := pipeline.SyncElements(wt.Vp8Depay, wt.RtpQueue); err != nil {
		return fmt.Errorf("failed to sync webrtc track elements: %w", err)
	}

	if err := wt.parent.pipeline.SwitchWebrtcInput(wt.SSRC); err != nil {
		return fmt.Errorf("failed to switch webrtc input to ssrc %d: %w", wt.SSRC, err)
	}
	return nil
}

// keyframeProbe detects keyframes and triggers switch, drops P-frames while waiting.
func (wt *WebrtcTrack) keyframeProbe(pad *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
	buffer := info.GetBuffer()
	if buffer == nil {
		return gst.PadProbeOK
	}

	pendingSSRC := wt.parent.pipeline.pendingSwitchSSRC
	isKeyframe := !buffer.HasFlags(gst.BufferFlagDeltaUnit)

	if isKeyframe {
		if !wt.HasKeyframe {
			wt.log.Infow("[SWITCH_DEBUG] First keyframe received on track", "ssrc", wt.SSRC)
		}
		wt.HasKeyframe = true

		if pendingSSRC == wt.SSRC {
			wt.log.Infow("[SWITCH_DEBUG] Keyframe detected, executing switch", "ssrc", wt.SSRC)
			buffer.SetFlags(gst.BufferFlagDiscont)
			wt.parent.pipeline.onTrackKeyframe(wt.SSRC)
		}
	} else if pendingSSRC == wt.SSRC {
		wt.parent.pipeline.checkPLIRetry(wt.SSRC)
		return gst.PadProbeDrop
	}

	return gst.PadProbeOK
}

// queueOutputProbe drops P-frames exiting queue before keyframe is seen.
func (wt *WebrtcTrack) queueOutputProbe(pad *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
	buffer := info.GetBuffer()
	if buffer == nil {
		return gst.PadProbeOK
	}

	if !wt.parent.pipeline.isActiveTrack(wt.SSRC) {
		return gst.PadProbeOK
	}

	isKeyframe := !buffer.HasFlags(gst.BufferFlagDeltaUnit)

	if isKeyframe {
		if !wt.SeenKeyframeInQueue {
			wt.log.Infow("[SWITCH_DEBUG] First keyframe exiting queue after switch", "ssrc", wt.SSRC)
		}
		wt.SeenKeyframeInQueue = true

		// Clear pending switch if keyframe was already buffered in queue
		if wt.parent.pipeline.pendingSwitchSSRC == wt.SSRC {
			wt.log.Infow("[SWITCH_DEBUG] Clearing pending switch - keyframe in queue", "ssrc", wt.SSRC)
			wt.parent.pipeline.pendingSwitchSSRC = 0
		}

		return gst.PadProbeOK
	}

	if !wt.SeenKeyframeInQueue {
		wt.log.Debugw("[SWITCH_DEBUG] Dropping stale P-frame from queue", "ssrc", wt.SSRC)
		return gst.PadProbeDrop
	}

	return gst.PadProbeOK
}

func (wt *WebrtcTrack) Close() error {
	if wt.SelPad != nil {
		active, err := wt.parent.InputSelector.GetProperty("active-pad")
		if err == nil && active != nil {
			if activePad, ok := active.(*gst.Pad); ok && activePad != nil {
				if activePad.GetName() == wt.SelPad.GetName() {
					wt.log.Warnw("Closing active track", nil, "ssrc", wt.SSRC)
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

	for _, elem := range []*gst.Element{
		wt.WebrtcRtpIn,
		wt.Vp8Depay,
		wt.RtpQueue,
		wt.WebrtcRtcpIn,
	} {
		if err := elem.SetState(gst.StateNull); err != nil {
			wt.log.Errorw("Failed to set webrtc track element to null state", err, "element", elem.GetName())
		}
	}

	wt.parent.pipeline.Pipeline().RemoveMany(
		wt.WebrtcRtpIn,
		wt.Vp8Depay,
		wt.RtpQueue,
		wt.WebrtcRtcpIn,
	)

	wt.log.Infow("Closed webrtc track", "ssrc", wt.SSRC)
	delete(wt.parent.Tracks, wt.SSRC)
	wt.log.Infow("Removed webrtc track from parent", "ssrc", wt.SSRC)
	return nil
}
