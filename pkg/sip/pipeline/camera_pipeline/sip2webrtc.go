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

	H264Depay     *gst.Element
	H264Parse     *gst.Element
	H264Dec       *gst.Element
	VideoConvert  *gst.Element
	VideoScale    *gst.Element
	ResFilter     *gst.Element
	VideoConvert2 *gst.Element
	VideoRate     *gst.Element
	FpsFilter     *gst.Element
	Vp8Enc        *gst.Element
	Vp8Pay        *gst.Element
	CapsFilter    *gst.Element
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
		"max-threads": int(4),
	})
	if err != nil {
		return fmt.Errorf("failed to create SIP h264 decoder: %w", err)
	}

	stw.VideoConvert, err = gst.NewElement("videoconvert")
	if err != nil {
		return fmt.Errorf("failed to create SIP videoconvert: %w", err)
	}

	stw.VideoScale, err = gst.NewElementWithProperties("videoscale", map[string]interface{}{
		"add-borders": true, // Add black bars for aspect ratio preservation
	})
	if err != nil {
		return fmt.Errorf("failed to create SIP videoscale: %w", err)
	}

	// Force 1280x720 resolution with PAR 1:1 - this forces letterboxing for non-16:9 content
	stw.ResFilter, err = gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString("video/x-raw,width=1280,height=720,pixel-aspect-ratio=1/1"),
	})
	if err != nil {
		return fmt.Errorf("failed to create SIP resolution capsfilter: %w", err)
	}

	// videoconvert after scaling to ensure proper format
	stw.VideoConvert2, err = gst.NewElement("videoconvert")
	if err != nil {
		return fmt.Errorf("failed to create SIP videoconvert2: %w", err)
	}

	// Force 24fps output
	stw.VideoRate, err = gst.NewElementWithProperties("videorate", map[string]interface{}{
		"drop-only": true, // Only drop frames, don't duplicate
	})
	if err != nil {
		return fmt.Errorf("failed to create SIP videorate: %w", err)
	}

	// Force 24fps in caps
	stw.FpsFilter, err = gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString("video/x-raw,framerate=24/1"),
	})
	if err != nil {
		return fmt.Errorf("failed to create SIP fps capsfilter: %w", err)
	}

	stw.Vp8Enc, err = gst.NewElementWithProperties("vp8enc", map[string]interface{}{
		"deadline":            int(1),
		"target-bitrate":      int(1_500_000),
		"cpu-used":            int(4),
		"keyframe-max-dist":   int(30),
		"lag-in-frames":       int(0),
		"threads":             int(4),
		"buffer-initial-size": int(100),
		"buffer-optimal-size": int(120),
		"buffer-size":         int(150),
		"min-quantizer":       int(4),
		"max-quantizer":       int(40),
		"cq-level":            int(13),
		"error-resilient":     int(1),
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
		stw.VideoConvert,
		stw.VideoScale,
		stw.ResFilter,
		stw.VideoConvert2,
		stw.VideoRate,
		stw.FpsFilter,
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
		stw.VideoConvert,
		stw.VideoScale,
		stw.ResFilter,
		stw.VideoConvert2,
		stw.VideoRate,
		stw.FpsFilter,
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
		stw.VideoConvert,
		stw.VideoScale,
		stw.ResFilter,
		stw.VideoConvert2,
		stw.VideoRate,
		stw.FpsFilter,
		stw.Vp8Enc,
		stw.Vp8Pay,
		stw.CapsFilter,
	); err != nil {
		return fmt.Errorf("failed to remove SIP to WebRTC elements from pipeline: %w", err)
	}
	return nil
}
