package camera_pipeline

import (
	"context"
	"fmt"

	"github.com/livekit/sip/pkg/sip/pipeline"
	"github.com/livekit/sip/pkg/sip/pipeline/event"

	"github.com/livekit/protocol/logger"
)

type CameraPipeline struct {
	*pipeline.BasePipeline
	*SipIo
	*WebrtcIo
	*SipToWebrtc
	*WebrtcToSip

	ctx    context.Context
	cancel context.CancelFunc
	loop   *event.EventLoop
}

func New(ctx context.Context, log logger.Logger) (*CameraPipeline, error) {
	log.Debugw("Creating camera pipeline")
	ctx, cancel := context.WithCancel(ctx)
	cp := &CameraPipeline{
		ctx:    ctx,
		cancel: cancel,
		loop:   event.NewEventLoop(ctx, log),
	}

	p, err := pipeline.New(log.WithComponent("camera_pipeline"))
	if err != nil {
		return nil, fmt.Errorf("failed to create gst pipeline: %w", err)
	}
	p.Log().Debugw("Setting bus to flushing")
	p.Pipeline().GetBus().SetFlushing(true)

	cp.BasePipeline = p

	p.Log().Debugw("Starting event loop")
	go cp.loop.Run()

	// p.Log().Debugw("Adding RTP bins")
	// cp.RtpBins, err = pipeline.AddChain(cp, NewRtpBins(log, cp))
	// if err != nil {
	// 	p.Log().Errorw("Failed to add RTP bins", err)
	// 	return nil, err
	// }
	p.Log().Debugw("Adding SIP IO chain")
	cp.SipIo, err = pipeline.AddChain(cp, NewSipInput(log, cp))
	if err != nil {
		p.Log().Errorw("Failed to add SIP IO chain", err)
		return nil, err
	}

	p.Log().Debugw("Adding Webrtc IO chain")
	cp.WebrtcIo, err = pipeline.AddChain(cp, NewWebrtcIo(log, cp))
	if err != nil {
		p.Log().Errorw("Failed to add WebRTC IO chain", err)
		return nil, err
	}

	p.Log().Debugw("Adding SIP to WebRTC chain")
	cp.SipToWebrtc, err = pipeline.AddChain(cp, NewSipToWebrtcChain(log, cp))
	if err != nil {
		p.Log().Errorw("Failed to add SIP to WebRTC chain", err)
		return nil, err
	}

	p.Log().Debugw("Adding WebRTC to SIP chain")
	cp.WebrtcToSip, err = pipeline.AddChain(cp, NewWebrtcToSipChain(log, cp))
	if err != nil {
		p.Log().Errorw("Failed to add WebRTC to SIP chain", err)
		return nil, err
	}

	p.Log().Debugw("Linking chains")
	if err := pipeline.LinkChains(cp,
		cp.SipIo,
		cp.WebrtcIo,
		cp.SipToWebrtc,
		cp.WebrtcToSip,
	); err != nil {
		p.Log().Errorw("Failed to link chains", err)
		return nil, err
	}

	p.Log().Debugw("Camera pipeline created")

	return cp, nil
}

func (cp *CameraPipeline) Close() error {
	closed := cp.Closed()

	cp.loop.Stop()

	if err := cp.BasePipeline.Close(); err != nil {
		return fmt.Errorf("failed to close camera pipeline: %w", err)
	}

	if !closed {
		cp.Log().Debugw("Closing camera pipeline chains")

		cp.Log().Debugw("Closing SIP IO")
		if err := cp.SipIo.Close(); err != nil {
			return fmt.Errorf("failed to close SIP IO: %w", err)
		}
		cp.SipIo = nil
		cp.Log().Debugw("Closing WebRTC IO")
		if err := cp.WebrtcIo.Close(); err != nil {
			return fmt.Errorf("failed to close WebRTC IO: %w", err)
		}
		cp.WebrtcIo = nil
		cp.Log().Debugw("Closing SIP to WebRTC chain")
		if err := cp.SipToWebrtc.Close(); err != nil {
			return fmt.Errorf("failed to close SIP to WebRTC chain: %w", err)
		}
		cp.SipToWebrtc = nil
		cp.Log().Debugw("Closing WebRTC to SIP chain")
		if err := cp.WebrtcToSip.Close(); err != nil {
			return fmt.Errorf("failed to close WebRTC to SIP chain: %w", err)
		}
		cp.WebrtcToSip = nil
	}
	cp.Log().Infow("Camera pipeline closed")

	return nil
}
