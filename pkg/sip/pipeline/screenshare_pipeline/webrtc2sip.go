package screenshare_pipeline

import (
	"fmt"

	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/sip/pkg/sip/pipeline"
)

// VP8CAPS is defined in webrtcIo.go

func NewWebrtcToSipChain(log logger.Logger, parent *ScreensharePipeline, sipOutPayloadType int) *WebrtcToSip {
	return &WebrtcToSip{
		log:               log.WithComponent("screenshare_webrtc_to_sip"),
		pipeline:          parent,
		sipOutPayloadType: sipOutPayloadType,
	}
}

type WebrtcToSip struct {
	log               logger.Logger
	pipeline          *ScreensharePipeline
	sipOutPayloadType int

	// Processing elements only - IO is in separate chains
	Vp8Depay     *gst.Element
	Vp8Dec       *gst.Element
	VideoConvert *gst.Element
	VideoRate    *gst.Element
	RateFilter   *gst.Element
	VideoScale   *gst.Element
	ScaleFilter  *gst.Element
	Queue        *gst.Element
	X264Enc      *gst.Element
	RtpH264Pay   *gst.Element
	OutQueue     *gst.Element
}

var _ pipeline.GstChain = (*WebrtcToSip)(nil)

// Create implements [pipeline.GstChain].
func (wts *WebrtcToSip) Create() error {
	var err error

	wts.Vp8Depay, err = gst.NewElementWithProperties("rtpvp8depay", map[string]interface{}{
		"request-keyframe": true,
	})
	if err != nil {
		return fmt.Errorf("failed to create vp8 depayloader: %w", err)
	}

	wts.Vp8Dec, err = gst.NewElementWithProperties("vp8dec", map[string]interface{}{})
	if err != nil {
		return fmt.Errorf("failed to create vp8 decoder: %w", err)
	}

	wts.VideoConvert, err = gst.NewElementWithProperties("videoconvert", map[string]interface{}{})
	if err != nil {
		return fmt.Errorf("failed to create videoconvert: %w", err)
	}

	wts.VideoRate, err = gst.NewElement("videorate")
	if err != nil {
		return fmt.Errorf("failed to create videorate: %w", err)
	}

	wts.RateFilter, err = gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString("video/x-raw,framerate=24/1"),
	})
	if err != nil {
		return fmt.Errorf("failed to create rate capsfilter: %w", err)
	}

	wts.VideoScale, err = gst.NewElementWithProperties("videoscale", map[string]interface{}{
		"add-borders": true, // Add black bars for aspect ratio preservation
	})
	if err != nil {
		return fmt.Errorf("failed to create videoscale: %w", err)
	}

	wts.ScaleFilter, err = gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString("video/x-raw,width=1280,height=720,pixel-aspect-ratio=1/1"),
	})
	if err != nil {
		return fmt.Errorf("failed to create scale capsfilter: %w", err)
	}

	wts.Queue, err = gst.NewElementWithProperties("queue", map[string]interface{}{
		"max-size-buffers": uint(3),
		"leaky":            int(2), // downstream
	})
	if err != nil {
		return fmt.Errorf("failed to create queue: %w", err)
	}

	wts.X264Enc, err = gst.NewElementWithProperties("x264enc", map[string]interface{}{
		"bitrate":      int(2048), // kbps
		"key-int-max":  int(24),   // Matches framerate for 1 keyframe/sec
		"speed-preset": int(1),    // ultrafast
		"tune":         int(4),    // zerolatency
	})
	if err != nil {
		return fmt.Errorf("failed to create x264 encoder: %w", err)
	}

	wts.RtpH264Pay, err = gst.NewElementWithProperties("rtph264pay", map[string]interface{}{
		"pt":              int(wts.sipOutPayloadType),
		"mtu":             int(1200),
		"config-interval": int(1),
		"aggregate-mode":  int(1), // zero-latency
	})
	if err != nil {
		return fmt.Errorf("failed to create rtp h264 payloader: %w", err)
	}

	wts.OutQueue, err = gst.NewElementWithProperties("queue", map[string]interface{}{
		"max-size-time": uint64(100000000), // 100ms
	})
	if err != nil {
		return fmt.Errorf("failed to create output queue: %w", err)
	}

	return nil
}

// Add implements [pipeline.GstChain].
func (wts *WebrtcToSip) Add() error {
	return wts.pipeline.Pipeline().AddMany(
		wts.Vp8Depay,
		wts.Vp8Dec,
		wts.VideoConvert,
		wts.VideoRate,
		wts.RateFilter,
		wts.VideoScale,
		wts.ScaleFilter,
		wts.Queue,
		wts.X264Enc,
		wts.RtpH264Pay,
		wts.OutQueue,
	)
}

// Link implements [pipeline.GstChain].
func (wts *WebrtcToSip) Link() error {
	if err := gst.ElementLinkMany(
		wts.Vp8Depay,
		wts.Vp8Dec,
		wts.VideoConvert,
		wts.VideoRate,
		wts.RateFilter,
		wts.VideoScale,
		wts.ScaleFilter,
		wts.Queue,
		wts.X264Enc,
		wts.RtpH264Pay,
		wts.OutQueue,
	); err != nil {
		return fmt.Errorf("failed to link WebrtcToSip elements: %w", err)
	}
	return nil
}

// Close implements [pipeline.GstChain].
func (wts *WebrtcToSip) Close() error {
	if err := wts.pipeline.Pipeline().RemoveMany(
		wts.Vp8Depay,
		wts.Vp8Dec,
		wts.VideoConvert,
		wts.VideoRate,
		wts.RateFilter,
		wts.VideoScale,
		wts.ScaleFilter,
		wts.Queue,
		wts.X264Enc,
		wts.RtpH264Pay,
		wts.OutQueue,
	); err != nil {
		return fmt.Errorf("failed to remove WebrtcToSip elements from pipeline: %w", err)
	}
	return nil
}
