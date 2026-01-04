package screenshare_pipeline

import (
	"fmt"

	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/sip/pkg/sip/pipeline"
)

const VP8CAPS = "application/x-rtp,media=video,encoding-name=VP8,clock-rate=90000,payload=96"

func NewWebrtcIo(log logger.Logger, parent *ScreensharePipeline) *WebrtcIo {
	return &WebrtcIo{
		log:      log.WithComponent("webrtc_io"),
		pipeline: parent,
	}
}

type WebrtcIo struct {
	log      logger.Logger
	pipeline *ScreensharePipeline

	WebrtcRtpIn *gst.Element // sourcereader
}

var _ pipeline.GstChain = (*WebrtcIo)(nil)

func (wio *WebrtcIo) Create() error {
	var err error
	wio.WebrtcRtpIn, err = gst.NewElementWithProperties("sourcereader", map[string]interface{}{
		"name":         "webrtc_rtp_in",
		"caps":         gst.NewCapsFromString(VP8CAPS),
		"do-timestamp": true,
	})
	if err != nil {
		return fmt.Errorf("failed to create webrtc rtp sourcereader: %w", err)
	}
	return nil
}

func (wio *WebrtcIo) Add() error {
	return wio.pipeline.Pipeline().AddMany(wio.WebrtcRtpIn)
}

func (wio *WebrtcIo) Link() error {
	// Link WebrtcRtpIn to WebrtcToSip.Vp8Depay
	if err := wio.WebrtcRtpIn.Link(wio.pipeline.WebrtcToSip.Vp8Depay); err != nil {
		return fmt.Errorf("failed to link webrtc rtp in to vp8 depayloader: %w", err)
	}
	return nil
}

func (wio *WebrtcIo) Close() error {
	return wio.pipeline.Pipeline().RemoveMany(wio.WebrtcRtpIn)
}
