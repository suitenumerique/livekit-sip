package camera_pipeline

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/sip/pkg/sip/pipeline"
	"github.com/livekit/sip/pkg/sip/pipeline/event"
)

const MaxKeyframeWaitTime = 2 * time.Second
const PLIRetryInterval = 200 * time.Millisecond

type CameraPipeline struct {
	*pipeline.BasePipeline
	*SipIo
	*WebrtcIo
	*SipToWebrtc
	*WebrtcToSip

	activeSSRC          atomic.Uint32
	pendingSwitchSSRC   atomic.Uint32
	needsEncoderReset   atomic.Bool
	ptMapConnected      bool
	switchStartTime     time.Time
	lastPLITime         time.Time
	switchTimer         *time.Timer
	sipKeyframeRequests chan struct{} // Channel for requesting SIP keyframes from goroutines
	switchRequests      chan uint32   // Channel for clean switch (IDR received)
	fallbackRequests    chan uint32   // Channel for fallback switch (timeout)
	pliRetryRequests    chan uint32   // Channel for PLI retry checks
}

func New(ctx context.Context, log logger.Logger) (*CameraPipeline, error) {
	log.Debugw("Creating camera pipeline")
	cp := &CameraPipeline{
		sipKeyframeRequests: make(chan struct{}, 1), // Buffered to avoid blocking, single slot for coalescing
		switchRequests:      make(chan uint32, 1),
		fallbackRequests:    make(chan uint32, 1),
		pliRetryRequests:    make(chan uint32, 1),
	}

	p, err := pipeline.New(ctx, log.WithComponent("camera_pipeline"), cp.cleanup)
	if err != nil {
		return nil, fmt.Errorf("failed to create gst pipeline: %w", err)
	}
	cp.BasePipeline = p

	// Bus watcher: log errors/warnings, recalculate latency on EventLoop
	bus := p.Pipeline().GetBus()
	recalcLatency := event.RegisterCallback(ctx, p.Loop(), func() {
		p.Pipeline().RecalculateLatency()
	})
	bus.AddWatch(func(msg *gst.Message) bool {
		switch msg.Type() {
		case gst.MessageError:
			gErr := msg.ParseError()
			if strings.Contains(gErr.Error(), "DTLS transport has not started") {
				p.Log().Debugw("GStreamer transient error", "detail", gErr.Error())
			} else {
				p.Log().Errorw("GStreamer pipeline error", gErr, "debug", gErr.DebugString())
			}
		case gst.MessageWarning:
			gWarn := msg.ParseWarning()
			p.Log().Warnw("GStreamer pipeline warning", gWarn)
		case gst.MessageLatency:
			recalcLatency()
		}
		return true
	})

	// Start goroutines to handle requests on the event loop
	go cp.sipKeyframeRequestHandler(ctx)
	cp.startSwitchHandlers(ctx)

	p.Log().Debugw("Starting event loop")

	p.Log().Debugw("Adding SIP IO chain")
	cp.SipIo, err = pipeline.AddChain(cp, NewSipInput(ctx, log, cp))
	if err != nil {
		p.Log().Errorw("Failed to add SIP IO chain", err)
		return nil, err
	}

	p.Log().Debugw("Adding Webrtc IO chain")
	cp.WebrtcIo, err = pipeline.AddChain(cp, NewWebrtcIo(ctx, log, cp))
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

func (cp *CameraPipeline) cleanup() error {
	if cp.BasePipeline == nil {
		return nil
	}

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

	cp.Log().Debugw("Camera pipeline chains closed")
	return nil
}

func (cp *CameraPipeline) Close() error {
	if err := cp.BasePipeline.Close(); err != nil {
		return fmt.Errorf("failed to close camera pipeline: %w", err)
	}
	cp.Log().Infow("Camera pipeline closed")
	return nil
}

// startSwitchHandlers launches one goroutine per channel to dispatch to the EventLoop.
func (cp *CameraPipeline) startSwitchHandlers(ctx context.Context) {
	go func() {
		handler := event.RegisterCallback(ctx, cp.Loop(), func(ssrc uint32) {
			if err := cp.executeSwitch(ssrc); err != nil {
				cp.Log().Errorw("switch execution failed", err, "ssrc", ssrc)
			}
		})
		for {
			select {
			case <-ctx.Done():
				return
			case ssrc := <-cp.switchRequests:
				handler(ssrc)
			}
		}
	}()
	go func() {
		handler := event.RegisterCallback(ctx, cp.Loop(), func(ssrc uint32) {
			cp.executeFallbackSwitch(ssrc)
		})
		for {
			select {
			case <-ctx.Done():
				return
			case ssrc := <-cp.fallbackRequests:
				handler(ssrc)
			}
		}
	}()
	go func() {
		handler := event.RegisterCallback(ctx, cp.Loop(), func(ssrc uint32) {
			cp.checkPLIRetry(ssrc)
		})
		for {
			select {
			case <-ctx.Done():
				return
			case ssrc := <-cp.pliRetryRequests:
				handler(ssrc)
			}
		}
	}()
}

// sipKeyframeRequestHandler processes SIP keyframe requests on the event loop thread
func (cp *CameraPipeline) sipKeyframeRequestHandler(ctx context.Context) {
	// Register a callback that will execute on the event loop
	handler := event.RegisterCallback(ctx, cp.Loop(), func() {
		cp.doRequestSipKeyframe()
	})

	for {
		select {
		case <-ctx.Done():
			return
		case <-cp.sipKeyframeRequests:
			handler()
		}
	}
}
