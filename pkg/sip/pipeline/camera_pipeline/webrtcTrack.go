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

	WebrtcRtpIn *gst.Element
	// rtpbin
	Vp8Depay *gst.Element
	RtpQueue *gst.Element

	WebrtcRtcpIn *gst.Element
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

// Add implements GstChain.
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

// Link implements GstChain.
func (wt *WebrtcTrack) Link() error {
	if err := pipeline.LinkPad(
		wt.WebrtcRtpIn.GetStaticPad("src"),
		wt.parent.RtpFunnel.GetRequestPad("sink_%u"),
	); err != nil {
		return fmt.Errorf("failed to link webrtc rtp queue to rtpbin: %w", err)
	}

	if err := pipeline.LinkPad(
		wt.WebrtcRtcpIn.GetStaticPad("src"),
		wt.parent.RtcpFunnel.GetRequestPad("sink_%u"),
	); err != nil {
		return fmt.Errorf("failed to link webrtc rtcp queue to rtcp funnel: %w", err)
	}

	return pipeline.SyncElements(
		wt.WebrtcRtpIn,
		wt.WebrtcRtcpIn,
	)
}

func (wt *WebrtcTrack) LinkParent(rtpbinPad *gst.Pad) error {
	if err := pipeline.LinkPad(
		rtpbinPad,
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

	if err := pipeline.LinkPad(
		wt.RtpQueue.GetStaticPad("src"),
		wt.parent.InputSelector.GetRequestPad("sink_%u"),
	); err != nil {
		return fmt.Errorf("failed to link webrtc rtp queue to input selector: %w", err)
	}

	if err := pipeline.SyncElements(
		wt.Vp8Depay,
		wt.RtpQueue,
	); err != nil {
		return fmt.Errorf("failed to sync webrtc track elements: %w", err)
	}

	if err := wt.parent.pipeline.DirtySwitchWebrtcInput(wt.SSRC); err != nil {
		return fmt.Errorf("failed to switch webrtc input to ssrc %d: %w", wt.SSRC, err)
	}
	return nil
}

// Close implements GstChain.
func (wt *WebrtcTrack) Close() error {
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
