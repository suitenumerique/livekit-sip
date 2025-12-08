package camera_pipeline

import (
	"fmt"

	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/app"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/sip/pkg/sip/pipeline"
)

type WebrtcTrack struct {
	log    logger.Logger
	parent *WebrtcToSip

	SSRC uint32

	RtpSrc   *gst.Element
	Vp8Depay *gst.Element
	RtpQueue *gst.Element

	RtcpSrc *gst.Element

	RtpAppSrc  *app.Source
	RtcpAppSrc *app.Source
	RtpPad     *gst.Pad
	RtcpPad    *gst.Pad
	RtpBinPad  *gst.Pad
	RtpSelPad  *gst.Pad
}

var _ pipeline.GstChain = (*WebrtcTrack)(nil)

func buildWebrtcTrack(log logger.Logger, parent *WebrtcToSip, ssrc uint32) (*WebrtcTrack, error) {
	log = log.WithValues("ssrc", ssrc)

	rtpSrc, err := gst.NewElementWithProperties("appsrc", map[string]interface{}{
		"name":         fmt.Sprintf("webrtc_rtp_in_%d", ssrc),
		"is-live":      true,
		"do-timestamp": true,
		"format":       int(gst.FormatTime),
		"max-bytes":    uint64(2_000_000),
		"block":        false,
		"caps":         gst.NewCapsFromString(VP8CAPS),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc rtp appsrc: %w", err)
	}

	vp8depay, err := gst.NewElementWithProperties("rtpvp8depay", map[string]interface{}{
		"request-keyframe": true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc vp8 depayloader: %w", err)
	}

	rtpQueue, err := gst.NewElementWithProperties("queue", map[string]interface{}{})
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc rtp queue: %w", err)
	}

	rtcpSrc, err := gst.NewElementWithProperties("appsrc", map[string]interface{}{
		"name":         fmt.Sprintf("webrtc_rtcp_in_%d", ssrc),
		"is-live":      true,
		"do-timestamp": true,
		"format":       int(gst.FormatTime),
		"max-bytes":    uint64(500_000),
		"block":        false,
		"caps":         gst.NewCapsFromString("application/x-rtcp"),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc rtcp appsrc: %w", err)
	}

	return &WebrtcTrack{
		log:    log,
		parent: parent,

		SSRC: ssrc,

		RtpSrc:   rtpSrc,
		Vp8Depay: vp8depay,
		RtpQueue: rtpQueue,

		RtcpSrc: rtcpSrc,

		RtpAppSrc:  app.SrcFromElement(rtpSrc),
		RtcpAppSrc: app.SrcFromElement(rtcpSrc),
	}, nil
}

// Add implements GstChain.
func (wt *WebrtcTrack) Add(pipeline *gst.Pipeline) error {
	if err := pipeline.AddMany(
		wt.RtpSrc,
		wt.Vp8Depay,
		wt.RtpQueue,
		wt.RtcpSrc,
	); err != nil {
		return fmt.Errorf("failed to add webrtc track elements to pipeline: %w", err)
	}
	return nil
}

// Link implements GstChain.
func (wt *WebrtcTrack) Link(p *gst.Pipeline) error {
	wt.RtpPad = wt.parent.RtpFunnel.GetRequestPad("sink_%u")
	if err := pipeline.LinkPad(
		wt.RtpSrc.GetStaticPad("src"),
		wt.RtpPad,
	); err != nil {
		return fmt.Errorf("failed to link webrtc rtp queue to rtpbin: %w", err)
	}

	if err := gst.ElementLinkMany(
		wt.Vp8Depay,
		wt.RtpQueue,
	); err != nil {
		return fmt.Errorf("failed to link webrtc track rtp elements: %w", err)
	}

	wt.RtcpPad = wt.parent.RtcpFunnel.GetRequestPad("sink_%u")
	if err := pipeline.LinkPad(
		wt.RtcpSrc.GetStaticPad("src"),
		wt.RtcpPad,
	); err != nil {
		return fmt.Errorf("failed to link webrtc rtcp queue to rtcp funnel: %w", err)
	}

	return wt.sync()
}

func (wt *WebrtcTrack) LinkParent(wts *WebrtcToSip, pad *gst.Pad) error {
	if err := pipeline.LinkPad(
		pad,
		wt.Vp8Depay.GetStaticPad("sink"),
	); err != nil {
		wt.log.Errorw("Failed to link rtpbin pad to depayloader", err)
		return err
	}

	wt.RtpSelPad = wts.InputSelector.GetRequestPad("sink_%u")
	if err := pipeline.LinkPad(
		wt.RtpQueue.GetStaticPad("src"),
		wt.RtpSelPad,
	); err != nil {
		wt.RtpSelPad = nil
		wt.log.Errorw("Failed to link rtpbin pad to depayloader", err)
		return err
	}
	wt.RtpBinPad = pad

	return wt.sync()
}

func (wt *WebrtcTrack) sync() error {
	for _, elem := range []*gst.Element{
		wt.RtpSrc,
		wt.Vp8Depay,
		wt.RtpQueue,
		wt.RtcpSrc,
	} {
		if ok := elem.SyncStateWithParent(); !ok {
			return fmt.Errorf("failed to sync state for %s", elem.GetName())
		}
	}
	return nil
}

// Close implements GstChain.
func (wt *WebrtcTrack) Close(p *gst.Pipeline) error {
	pipeline.ReleasePad(wt.RtpPad)
	pipeline.ReleasePad(wt.RtcpPad)
	pipeline.ReleasePad(wt.RtpSelPad)
	pipeline.ReleasePad(wt.RtpBinPad)

	p.RemoveMany(
		wt.RtpSrc,
		wt.Vp8Depay,
		wt.RtpQueue,
		wt.RtcpSrc,
	)
	return nil
}
