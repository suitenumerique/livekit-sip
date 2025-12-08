package camera_pipeline

import (
	"fmt"

	"github.com/livekit/protocol/logger"
	"github.com/livekit/sip/pkg/sip/pipeline"
)

type CameraPipeline struct {
	*pipeline.BasePipeline

	SipToWebrtc *SipToWebrtc
	WebrtcToSip *WebrtcToSip
}

func New(log logger.Logger, sipPt uint8) (*CameraPipeline, error) {
	cp := &CameraPipeline{}

	p, err := pipeline.New(log)
	if err != nil {
		return nil, fmt.Errorf("failed to create gst pipeline: %w", err)
	}

	cp.BasePipeline = p

	cp.SipToWebrtc, err = pipeline.CastErr[*SipToWebrtc](cp.AddChain(buildSipToWebRTCChain(log.WithComponent("sip_to_webrtc"), int(sipPt))))
	if err != nil {
		return nil, err
	}

	cp.WebrtcToSip, err = pipeline.CastErr[*WebrtcToSip](cp.AddChain(buildSelectorToSipChain(log.WithComponent("selector_to_sip"), int(sipPt))))
	if err != nil {
		return nil, err
	}

	if err := cp.setupAutoSwitching(); err != nil {
		return nil, err
	}

	return cp, nil
}
