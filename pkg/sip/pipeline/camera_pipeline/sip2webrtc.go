package camera_pipeline

import (
	"fmt"

	"github.com/livekit/sip/pkg/sip/pipeline"

	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/logger"
)

func NewSipToWebrtcChain(log logger.Logger, parent *CameraPipeline) *SipToWebrtc {
	return &SipToWebrtc{
		log:      log,
		pipeline: parent,
	}
}

type SipToWebrtc struct {
	pipeline *CameraPipeline
	log      logger.Logger

	H264Depay         *gst.Element
	H264Parse         *gst.Element
	H264Dec           *gst.Element
	VideoConvertScale *gst.Element
	VideoRate         *gst.Element
	RateFilter        *gst.Element
	ResFilter         *gst.Element
	Queue             *gst.Element
	Vp8Enc            *gst.Element
	Vp8Pay            *gst.Element
	CapsFilter        *gst.Element
}

var _ pipeline.GstChain = (*SipToWebrtc)(nil)

// Create implements [pipeline.GstChain].
func (stw *SipToWebrtc) Create() error {
	var err error

	stw.H264Depay, err = gst.NewElementWithProperties("rtph264depay", map[string]interface{}{
		"request-keyframe": true,
	})
	if err != nil {
		return fmt.Errorf("failed to create SIP rtp depayloader: %w", err)
	}

	stw.H264Parse, err = gst.NewElementWithProperties("h264parse", map[string]interface{}{
		"config-interval": int(1),
	})
	if err != nil {
		return fmt.Errorf("failed to create SIP h264 parser: %w", err)
	}

	stw.H264Dec, err = gst.NewElementWithProperties("avdec_h264", map[string]interface{}{
		"max-threads": int(2),
	})
	if err != nil {
		return fmt.Errorf("failed to create SIP h264 decoder: %w", err)
	}

	// Single element for convert + scale (avoids intermediate pixel format conversion)
	stw.VideoConvertScale, err = gst.NewElementWithProperties("videoconvertscale", map[string]interface{}{
		"add-borders": true,
	})
	if err != nil {
		return fmt.Errorf("failed to create SIP videoconvertscale: %w", err)
	}

	stw.VideoRate, err = gst.NewElementWithProperties("videorate", map[string]interface{}{
		"drop-only": true,
	})
	if err != nil {
		return fmt.Errorf("failed to create SIP videorate: %w", err)
	}

	stw.RateFilter, err = gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString("video/x-raw,framerate=24/1"),
	})
	if err != nil {
		return fmt.Errorf("failed to create SIP rate capsfilter: %w", err)
	}

	// Force 1280x720 I420 output — I420 is what vp8enc expects
	stw.ResFilter, err = gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString("video/x-raw,format=I420,width=1280,height=720,pixel-aspect-ratio=1/1"),
	})
	if err != nil {
		return fmt.Errorf("failed to create SIP resolution capsfilter: %w", err)
	}

	stw.Queue, err = gst.NewElementWithProperties("queue", map[string]interface{}{
		"max-size-buffers": uint(5),
		"max-size-time":    uint64(250000000),
		"leaky":            int(2),
	})
	if err != nil {
		return fmt.Errorf("failed to create SIP to WebRTC queue: %w", err)
	}

	stw.Vp8Enc, err = gst.NewElementWithProperties("vp8enc", map[string]interface{}{
		"deadline":            int(1), // realtime
		"target-bitrate":      int(2_000_000),
		"cpu-used":            int(8),
		"keyframe-max-dist":   int(12),
		"lag-in-frames":       int(0),
		"threads":             int(2),
		"token-partitions":    int(2),   // Enable 4 partitions for multi-threaded encoding
		"buffer-initial-size": int(200), // Increased for long sessions
		"buffer-optimal-size": int(300), // Increased for long sessions
		"buffer-size":         int(500), // Increased for long sessions
		"min-quantizer":       int(4),
		"max-quantizer":       int(32),
		"cq-level":            int(10),
		"error-resilient":     int(1),
		"end-usage":           int(1), // CBR
	})
	if err != nil {
		return fmt.Errorf("failed to create SIP vp8 encoder: %w", err)
	}

	stw.Vp8Pay, err = gst.NewElementWithProperties("rtpvp8pay", map[string]interface{}{
		"pt":              int(96),
		"mtu":             int(1200),
		"picture-id-mode": int(2), // 15-bit in your launch string
	})
	if err != nil {
		return fmt.Errorf("failed to create SIP rtp vp8 payloader: %w", err)
	}

	stw.CapsFilter, err = gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString("application/x-rtp,media=video,encoding-name=VP8,clock-rate=90000,rtcp-fb-nack=1,rtcp-fb-nack-pli=1,rtcp-fb-ccm-fir=1,rtcp-fb-transport-cc=1,extmap-3=\"http://www.ietf.org/id/draft-holmer-rmcat-transport-wide-cc-extensions-01\""),
	})
	if err != nil {
		return fmt.Errorf("failed to create SIP to WebRTC capsfilter: %w", err)
	}

	return nil
}

func (stw *SipToWebrtc) Add() error {
	if err := stw.pipeline.Pipeline().AddMany(
		stw.H264Depay,
		stw.H264Parse,
		stw.H264Dec,
		stw.VideoConvertScale,
		stw.VideoRate,
		stw.RateFilter,
		stw.ResFilter,
		stw.Queue,
		stw.Vp8Enc,
		stw.Vp8Pay,
		stw.CapsFilter,
	); err != nil {
		return fmt.Errorf("failed to add SIP to WebRTC elements to pipeline: %w", err)
	}
	return nil
}

func (stw *SipToWebrtc) Link() error {
	if err := gst.ElementLinkMany(
		stw.H264Depay,
		stw.H264Parse,
		stw.H264Dec,
		stw.VideoConvertScale,
		stw.VideoRate,
		stw.RateFilter,
		stw.ResFilter,
		stw.Queue,
		stw.Vp8Enc,
		stw.Vp8Pay,
		stw.CapsFilter,
	); err != nil {
		return fmt.Errorf("failed to link sip to webrtc: %w", err)
	}

	return nil
}

func (stw *SipToWebrtc) Close() error {
	if err := stw.pipeline.Pipeline().RemoveMany(
		stw.H264Depay,
		stw.H264Parse,
		stw.H264Dec,
		stw.VideoConvertScale,
		stw.VideoRate,
		stw.RateFilter,
		stw.ResFilter,
		stw.Queue,
		stw.Vp8Enc,
		stw.Vp8Pay,
		stw.CapsFilter,
	); err != nil {
		return fmt.Errorf("failed to remove SIP to WebRTC elements from pipeline: %w", err)
	}
	return nil
}
