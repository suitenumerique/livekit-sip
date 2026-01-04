package screenshare_pipeline

import (
	"context"
	"fmt"
	"io"
	"net"
	"runtime/cgo"

	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/sip/pkg/sip/pipeline"
)

type ScreensharePipeline struct {
	*pipeline.BasePipeline
	*WebrtcIo     // Input chain
	*WebrtcToSip  // Processing chain
	*SipIo        // Output chain

	ctx    context.Context
	cancel context.CancelFunc

	// Callback for when WebRTC track ends (EOS)
	OnTrackEnded func()
}

func New(ctx context.Context, log logger.Logger, sipPayloadType uint8) (*ScreensharePipeline, error) {
	log.Debugw("Creating screenshare pipeline")
	ctx, cancel := context.WithCancel(ctx)
	sp := &ScreensharePipeline{
		ctx:    ctx,
		cancel: cancel,
	}

	p, err := pipeline.New(ctx, log.WithComponent("screenshare_pipeline"), sp.cleanup)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to create gst pipeline: %w", err)
	}
	p.Log().Debugw("Setting bus to flushing")
	p.Pipeline().GetBus().SetFlushing(true)

	sp.BasePipeline = p

	p.Log().Debugw("Adding WebRTC IO chain")
	sp.WebrtcIo, err = pipeline.AddChain(sp, NewWebrtcIo(log, sp))
	if err != nil {
		p.Log().Errorw("Failed to add WebRTC IO chain", err)
		cancel()
		return nil, err
	}

	p.Log().Debugw("Adding WebRTC to SIP chain")
	sp.WebrtcToSip, err = pipeline.AddChain(sp, NewWebrtcToSipChain(log, sp, int(sipPayloadType)))
	if err != nil {
		p.Log().Errorw("Failed to add WebRTC to SIP chain", err)
		cancel()
		return nil, err
	}

	p.Log().Debugw("Adding SIP IO chain")
	sp.SipIo, err = pipeline.AddChain(sp, NewSipIo(log, sp))
	if err != nil {
		p.Log().Errorw("Failed to add SIP IO chain", err)
		cancel()
		return nil, err
	}

	p.Log().Debugw("Linking chains")
	if err := pipeline.LinkChains(sp,
		sp.WebrtcIo,
		sp.WebrtcToSip,
		sp.SipIo,
	); err != nil {
		p.Log().Errorw("Failed to link chains", err)
		cancel()
		return nil, err
	}

	p.Log().Debugw("Screenshare pipeline created")

	return sp, nil
}

func (sp *ScreensharePipeline) cleanup() error {
	if sp.BasePipeline == nil {
		return nil // edge case where pipeline close right after creation
	}

	sp.Log().Debugw("Closing screenshare pipeline chains")

	sp.Log().Debugw("Closing WebRTC IO")
	if err := sp.WebrtcIo.Close(); err != nil {
		return fmt.Errorf("failed to close WebRTC IO: %w", err)
	}
	sp.WebrtcIo = nil

	sp.Log().Debugw("Closing WebRTC to SIP chain")
	if err := sp.WebrtcToSip.Close(); err != nil {
		return fmt.Errorf("failed to close WebRTC to SIP chain: %w", err)
	}
	sp.WebrtcToSip = nil

	sp.Log().Debugw("Closing SIP IO")
	if err := sp.SipIo.Close(); err != nil {
		return fmt.Errorf("failed to close SIP IO: %w", err)
	}
	sp.SipIo = nil

	sp.Log().Debugw("Screenshare pipeline chains closed")
	return nil
}

func (sp *ScreensharePipeline) Close() error {
	// BasePipeline.Close() will set the pipeline to NULL state, which triggers
	// Unlock() on all source elements. The Unlock() calls Close() on the readers
	// which sets a read deadline to unblock any pending Read() calls.
	if err := sp.BasePipeline.Close(); err != nil {
		return fmt.Errorf("failed to close screenshare pipeline: %w", err)
	}
	sp.Log().Infow("Screenshare pipeline closed")

	return nil
}

// SipIO sets the SIP output writer for the screenshare pipeline.
// Only RTP output is needed for WebRTC → SIP direction.
func (sp *ScreensharePipeline) SipIO(rtp, rtcp net.Conn, pt uint8) error {
	rtpHandle := cgo.NewHandle(rtp)
	defer rtpHandle.Delete()

	h264Caps := fmt.Sprintf(
		"application/x-rtp,media=video,encoding-name=H264,payload=%d,clock-rate=90000",
		pt)

	sp.Log().Infow("Setting SIP IO for screenshare",
		"rtp_remote", rtp.RemoteAddr(),
		"rtp_local", rtp.LocalAddr(),
		"caps", h264Caps,
	)

	// Set output caps
	if err := sp.SipIo.SipRtpOut.SetProperty("caps",
		gst.NewCapsFromString(h264Caps),
	); err != nil {
		return fmt.Errorf("failed to set sip rtp out caps (pt: %d): %w", pt, err)
	}

	// Set the SIP output handle (writer)
	if err := sp.SipIo.SipRtpOut.SetProperty("handle", uint64(rtpHandle)); err != nil {
		return fmt.Errorf("failed to set sip rtp out handle: %w", err)
	}

	return sp.checkReady()
}

// SetWebrtcInput sets the WebRTC track input reader.
func (sp *ScreensharePipeline) SetWebrtcInput(rtp io.ReadCloser) error {
	rtpHandle := cgo.NewHandle(rtp)
	defer rtpHandle.Delete()

	sp.Log().Infow("Setting WebRTC input for screenshare")

	// Set the WebRTC input handle (reader)
	if err := sp.WebrtcIo.WebrtcRtpIn.SetProperty("handle", uint64(rtpHandle)); err != nil {
		return fmt.Errorf("failed to set webrtc rtp in handle: %w", err)
	}

	return sp.checkReady()
}

func (sp *ScreensharePipeline) checkReady() error {
	if sp.Pipeline().GetCurrentState() != gst.StatePaused {
		// Already playing or not ready yet
		return nil
	}

	checkHandle := func(elem *gst.Element) bool {
		hasHandleVal, err := elem.GetProperty("has-handle")
		if err != nil {
			return false
		}
		hasHandle, ok := hasHandleVal.(bool)
		return ok && hasHandle
	}

	// Check if both input and output handles are set
	ready := checkHandle(sp.WebrtcIo.WebrtcRtpIn) && checkHandle(sp.SipIo.SipRtpOut)

	if ready {
		sp.Log().Infow("All handles ready, setting screenshare pipeline to PLAYING")
		if err := sp.SetState(gst.StatePlaying); err != nil {
			return fmt.Errorf("failed to set screenshare pipeline to playing: %w", err)
		}

		// Sync element states
		for _, e := range []*gst.Element{
			sp.WebrtcIo.WebrtcRtpIn,
			sp.SipIo.SipRtpOut,
		} {
			if !e.SyncStateWithParent() {
				return fmt.Errorf("failed to sync state with parent for element %s", e.GetName())
			}
		}
	}
	return nil
}
