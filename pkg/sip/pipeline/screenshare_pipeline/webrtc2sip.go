package screenshare_pipeline

import (
	"fmt"

	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/app"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/sip/pkg/sip/pipeline"
)

const VP8CAPS = "application/x-rtp,media=video,encoding-name=VP8,clock-rate=90000,payload=96,rtcp-fb-nack-pli=true"

var webrtcCaps = map[uint]string{
	96: VP8CAPS,
}

type WebrtcToSip struct {
	log logger.Logger

	WebrtcRtpSrc *gst.Element
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
	SipRtpSink   *gst.Element

	WebrtcRtpAppSrc *app.Source
	SipRtpAppSink   *app.Sink
}

var _ pipeline.GstChain = (*WebrtcToSip)(nil)

func buildWebrtcToSipChain(log logger.Logger, sipOutPayloadType int) (*WebrtcToSip, error) {
	WebrtcRtpSrc, err := gst.NewElementWithProperties("appsrc", map[string]interface{}{
		"is-live":      true,
		"do-timestamp": true,
		"format":       int(gst.FormatTime),
		"max-bytes":    uint64(2_000_000),
		"block":        false,
		"caps":         gst.NewCapsFromString(VP8CAPS),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc rtp appsrc: %w", err)
	}

	vp8depay, err := gst.NewElementWithProperties("rtpvp8depay", map[string]interface{}{
		"request-keyframe": true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc vp8 depayloader: %w", err)
	}

	vp8dec, err := gst.NewElementWithProperties("vp8dec", map[string]interface{}{})
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc vp8 decoder: %w", err)
	}

	videoconvert, err := gst.NewElementWithProperties("videoconvert", map[string]interface{}{})
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc videoconvert: %w", err)
	}

	videorate, err := gst.NewElement("videorate")
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc videorate: %w", err)
	}

	ratefilter, err := gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString("video/x-raw,framerate=24/1"),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc rate capsfilter: %w", err)
	}

	videoscale, err := gst.NewElementWithProperties("videoscale", map[string]interface{}{
		"add-borders": true, // Add black bars for aspect ratio preservation
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc videoscale: %w", err)
	}

	scalefilter, err := gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString("video/x-raw,width=1280,height=720,pixel-aspect-ratio=1/1"),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc scale capsfilter: %w", err)
	}

	queue, err := gst.NewElementWithProperties("queue", map[string]interface{}{
		"max-size-buffers": uint(3),
		"leaky":            int(2), // downstream
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc queue: %w", err)
	}

	x264enc, err := gst.NewElementWithProperties("x264enc", map[string]interface{}{
		"bitrate":      int(2048), // kbps
		"key-int-max":  int(24),   // Matches framerate for 1 keyframe/sec
		"speed-preset": int(1),    // ultrafast
		"tune":         int(4),    // zerolatency
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc x264 encoder: %w", err)
	}

	rtph264pay, err := gst.NewElementWithProperties("rtph264pay", map[string]interface{}{
		"pt":              int(sipOutPayloadType),
		"mtu":             int(1200),
		"config-interval": int(1),
		"aggregate-mode":  int(1), // zero-latency
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc rtp h264 payloader: %w", err)
	}

	outQueue, err := gst.NewElementWithProperties("queue", map[string]interface{}{
		"max-size-time": uint64(100000000), // 100ms
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc rtcp out queue: %w", err)
	}

	sipRtpSink, err := gst.NewElementWithProperties("appsink", map[string]interface{}{
		"name":         "sip_rtp_out",
		"emit-signals": false,
		"drop":         false,
		"max-buffers":  uint(100),
		"sync":         false,
		"async":        false, // Don't wait for preroll - live pipeline
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc appsink: %w", err)
	}

	return &WebrtcToSip{
		log: log,

		WebrtcRtpSrc: WebrtcRtpSrc,
		Vp8Depay:     vp8depay,
		Vp8Dec:       vp8dec,
		VideoConvert: videoconvert,
		VideoRate:    videorate,
		RateFilter:   ratefilter,
		VideoScale:   videoscale,
		ScaleFilter:  scalefilter,
		Queue:        queue,
		X264Enc:      x264enc,
		RtpH264Pay:   rtph264pay,
		OutQueue:     outQueue,
		SipRtpSink:   sipRtpSink,

		WebrtcRtpAppSrc: app.SrcFromElement(WebrtcRtpSrc),
		SipRtpAppSink:   app.SinkFromElement(sipRtpSink),
	}, nil
}

// Add implements GstChain.
func (wts *WebrtcToSip) Add(pipeline *gst.Pipeline) error {
	if err := pipeline.AddMany(
		wts.WebrtcRtpSrc,
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
		wts.SipRtpSink,
	); err != nil {
		return fmt.Errorf("failed to add SelectorToSip elements to pipeline: %w", err)
	}
	return nil
}

// Link implements GstChain.
func (wts *WebrtcToSip) Link(p *gst.Pipeline) error {
	if err := gst.ElementLinkMany(
		wts.WebrtcRtpSrc,
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
		wts.SipRtpSink,
	); err != nil {
		return fmt.Errorf("failed to link SelectorToSip elements: %w", err)
	}
	return nil
}

// Close implements GstChain.
func (wts *WebrtcToSip) Close(pipeline *gst.Pipeline) error {
	if err := pipeline.RemoveMany(
		wts.WebrtcRtpSrc,
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
		wts.SipRtpSink,
	); err != nil {
		return fmt.Errorf("failed to remove SelectorToSip elements from pipeline: %w", err)
	}
	return nil
}
