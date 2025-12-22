package camera_pipeline

import (
	"fmt"

	"github.com/livekit/sip/pkg/sip/pipeline"

	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/logger"
)

func NewWebrtcToSipChain(log logger.Logger, parent *CameraPipeline) *WebrtcToSip {
	return &WebrtcToSip{
		log:      log,
		pipeline: parent,
	}
}

type WebrtcToSip struct {
	pipeline *CameraPipeline
	log      logger.Logger

	Vp8Dec      *gst.Element
	VideoRate   *gst.Element
	RateFilter  *gst.Element
	VideoScale  *gst.Element
	ScaleFilter *gst.Element
	Queue       *gst.Element
	X264Enc     *gst.Element
	RtpH264Pay  *gst.Element
}

var _ pipeline.GstChain = (*WebrtcToSip)(nil)

// Create implements [pipeline.GstChain].
func (stw *WebrtcToSip) Create() error {
	var err error
	stw.Vp8Dec, err = gst.NewElementWithProperties("vp8dec", map[string]interface{}{})
	if err != nil {
		return fmt.Errorf("failed to create webrtc vp8 decoder: %w", err)
	}

	stw.VideoRate, err = gst.NewElement("videorate")
	if err != nil {
		return fmt.Errorf("failed to create webrtc videorate: %w", err)
	}

	stw.RateFilter, err = gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString("video/x-raw,framerate=24/1"),
	})
	if err != nil {
		return fmt.Errorf("failed to create webrtc rate capsfilter: %w", err)
	}

	stw.VideoScale, err = gst.NewElementWithProperties("videoscale", map[string]interface{}{
		"add-borders": true, // Add black bars for aspect ratio preservation
	})
	if err != nil {
		return fmt.Errorf("failed to create webrtc videoscale: %w", err)
	}

	stw.ScaleFilter, err = gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString("video/x-raw,width=1280,height=720,pixel-aspect-ratio=1/1"),
	})
	if err != nil {
		return fmt.Errorf("failed to create webrtc scale capsfilter: %w", err)
	}

	stw.Queue, err = gst.NewElementWithProperties("queue", map[string]interface{}{})
	if err != nil {
		return fmt.Errorf("failed to create webrtc queue: %w", err)
	}

	stw.X264Enc, err = gst.NewElementWithProperties("x264enc", map[string]interface{}{
		"bitrate":      int(2000),
		"key-int-max":  int(48), // Matches framerate for 1 keyframe/sec
		"speed-preset": int(1),  // ultrafast
		"tune":         int(4),  // zerolatency
	})
	if err != nil {
		return fmt.Errorf("failed to create webrtc x264 encoder: %w", err)
	}

	stw.RtpH264Pay, err = gst.NewElementWithProperties("rtph264pay", map[string]interface{}{
		"mtu":             int(1200),
		"config-interval": int(1),
		"aggregate-mode":  int(1), // zero-latency
	})
	if err != nil {
		return fmt.Errorf("failed to create webrtc rtp h264 payloader: %w", err)
	}

	return nil
}

func (stw *WebrtcToSip) Add() error {
	if err := stw.pipeline.Pipeline().AddMany(
		stw.Vp8Dec,
		stw.VideoRate,
		stw.RateFilter,
		stw.VideoScale,
		stw.ScaleFilter,
		stw.Queue,
		stw.X264Enc,
		stw.RtpH264Pay,
	); err != nil {
		return fmt.Errorf("failed to add WebRTC to SIP elements to pipeline: %w", err)
	}
	return nil
}

func (stw *WebrtcToSip) Link() error {
	if err := gst.ElementLinkMany(
		stw.Vp8Dec,
		stw.VideoRate,
		stw.RateFilter,
		stw.VideoScale,
		stw.ScaleFilter,
		stw.Queue,
		stw.X264Enc,
		stw.RtpH264Pay,
	); err != nil {
		return fmt.Errorf("failed to link WebRTC to SIP elements: %w", err)
	}
	return nil
}

func (stw *WebrtcToSip) Close() error {
	if err := stw.pipeline.Pipeline().RemoveMany(
		stw.Vp8Dec,
		stw.VideoRate,
		stw.RateFilter,
		stw.VideoScale,
		stw.ScaleFilter,
		stw.Queue,
		stw.X264Enc,
		stw.RtpH264Pay,
	); err != nil {
		return fmt.Errorf("failed to remove WebRTC to SIP elements from pipeline: %w", err)
	}
	return nil
}
