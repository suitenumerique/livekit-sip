package screenshare_pipeline

import (
	"fmt"

	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/sip/pkg/sip/pipeline"
)

func NewSipIo(log logger.Logger, parent *ScreensharePipeline) *SipIo {
	return &SipIo{
		log:      log.WithComponent("sip_io"),
		pipeline: parent,
	}
}

type SipIo struct {
	log      logger.Logger
	pipeline *ScreensharePipeline

	SipRtpOut *gst.Element // sinkwriter
}

var _ pipeline.GstChain = (*SipIo)(nil)

func (sio *SipIo) Create() error {
	var err error
	sio.SipRtpOut, err = gst.NewElementWithProperties("sinkwriter", map[string]interface{}{
		"name":  "sip_rtp_out",
		"sync":  false,
		"async": false,
	})
	if err != nil {
		return fmt.Errorf("failed to create sip rtp sinkwriter: %w", err)
	}
	return nil
}

func (sio *SipIo) Add() error {
	return sio.pipeline.Pipeline().AddMany(sio.SipRtpOut)
}

func (sio *SipIo) Link() error {
	// Link WebrtcToSip.OutQueue to SipRtpOut
	if err := sio.pipeline.WebrtcToSip.OutQueue.Link(sio.SipRtpOut); err != nil {
		return fmt.Errorf("failed to link webrtc to sip out queue to sip rtp out: %w", err)
	}
	return nil
}

func (sio *SipIo) Close() error {
	return sio.pipeline.Pipeline().RemoveMany(sio.SipRtpOut)
}
