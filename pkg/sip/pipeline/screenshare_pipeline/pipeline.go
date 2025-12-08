package screenshare_pipeline

import (
	"fmt"

	"github.com/livekit/protocol/logger"
	"github.com/livekit/sip/pkg/sip/pipeline"
)

type ScreensharePipeline struct {
	*pipeline.BasePipeline

	WebrtcToSip *WebrtcToSip
}

func New(log logger.Logger, sipPt uint8) (*ScreensharePipeline, error) {
	cp := &ScreensharePipeline{}

	log.Debugw("screenshare.pipeline.create", "pt", sipPt)

	p, err := pipeline.New(log)
	if err != nil {
		return nil, fmt.Errorf("failed to create gst pipeline: %w", err)
	}

	cp.BasePipeline = p

	cp.WebrtcToSip, err = pipeline.CastErr[*WebrtcToSip](cp.AddChain(buildWebrtcToSipChain(log.WithComponent("screenshare_webrtc_to_sip"), int(sipPt))))
	if err != nil {
		return nil, err
	}

	return cp, nil
}
