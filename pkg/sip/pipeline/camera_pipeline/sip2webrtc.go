package camera_pipeline

import (
	"fmt"
	"strings"

	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/app"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/sip/pkg/sip/pipeline"
)

type SipToWebrtc struct {
	log logger.Logger

	RtpBin *gst.Element

	SipRtpSrc     *gst.Element
	H264Depay     *gst.Element
	H264Parse     *gst.Element
	AvdecH264     *gst.Element
	VideoConvert  *gst.Element
	VideoRate     *gst.Element
	RateFilter    *gst.Element
	VideoScale    *gst.Element
	ScaleFilter   *gst.Element
	Queue         *gst.Element
	Vp8Enc        *gst.Element
	RtpVp8Pay     *gst.Element
	OutQueue      *gst.Element
	WebrtcRtpSink *gst.Element

	SipRtcpSrc  *gst.Element
	SipRtcpSink *gst.Element

	SipRtpAppSrc     *app.Source
	WebrtcRtpAppSink *app.Sink
	SipRtcpAppSrc    *app.Source
	SipRtcpAppSink   *app.Sink
}

var _ pipeline.GstChain = (*SipToWebrtc)(nil)

func buildSipToWebRTCChain(log logger.Logger, sipPayloadType int) (*SipToWebrtc, error) {
	rtpBin, err := gst.NewElementWithProperties("rtpbin", map[string]interface{}{
		"autoremove":         true,
		"do-lost":            true,
		"do-sync-event":      true,
		"drop-on-latency":    true,
		"latency":            uint64(0),
		"rtcp-sync-interval": uint64(1000000000), // 1s
		"rtp-profile":        int(3),             // RTP_PROFILE_AVPF
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create SIP rtpbin: %w", err)
	}

	sipRtpSrc, err := gst.NewElementWithProperties("appsrc", map[string]interface{}{
		"name":         "sip_rtp_in",
		"is-live":      true,
		"do-timestamp": true,
		"format":       int(gst.FormatTime),
		"max-bytes":    uint64(2_000_000),
		"block":        false,
		"caps": gst.NewCapsFromString(fmt.Sprintf(
			"application/x-rtp,media=video,encoding-name=H264,payload=%d,clock-rate=90000",
			sipPayloadType,
		)),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create SIP appsrc: %w", err)
	}

	h264depay, err := gst.NewElementWithProperties("rtph264depay", map[string]interface{}{
		"request-keyframe": true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create SIP rtp depayloader: %w", err)
	}

	h264parse, err := gst.NewElementWithProperties("h264parse", map[string]interface{}{
		"config-interval": int(1),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create SIP h264 parser: %w", err)
	}

	avdecH264, err := gst.NewElementWithProperties("avdec_h264", map[string]interface{}{
		"max-threads": int(4),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create SIP h264 decoder: %w", err)
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
		"add-borders": true,
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

	vp8enc, err := gst.NewElementWithProperties("vp8enc", map[string]interface{}{
		"deadline":            int(1),
		"target-bitrate":      int(1_024_000),
		"cpu-used":            int(4),
		"keyframe-max-dist":   int(24),
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
		return nil, fmt.Errorf("failed to create SIP vp8 encoder: %w", err)
	}

	rtpVp8Pay, err := gst.NewElementWithProperties("rtpvp8pay", map[string]interface{}{
		"pt":              int(96),
		"mtu":             int(1200),
		"picture-id-mode": int(2), // 15-bit in your launch string
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create SIP rtp vp8 payloader: %w", err)
	}

	outQueue, err := gst.NewElementWithProperties("queue", map[string]interface{}{
		"max-size-time": uint64(100000000), // 100ms
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc rtcp out queue: %w", err)
	}

	webrtcRtpSink, err := gst.NewElementWithProperties("appsink", map[string]interface{}{
		"name":         "webrtc_rtp_out",
		"emit-signals": false,
		"drop":         false,
		"max-buffers":  uint(100),
		"sync":         false,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create SIP appsink: %w", err)
	}

	sipRtcpSrc, err := gst.NewElementWithProperties("appsrc", map[string]interface{}{
		"name":         "sip_rtcp_in",
		"is-live":      true,
		"do-timestamp": true,
		"format":       int(3),
		"max-bytes":    uint64(200_000),
		"block":        false,
		"caps":         gst.NewCapsFromString("application/x-rtcp"),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create SIP rtcp appsrc: %w", err)
	}

	sipRtcpSink, err := gst.NewElementWithProperties("appsink", map[string]interface{}{
		"name":         "sip_rtcp_out",
		"emit-signals": false,
		"drop":         false,
		"max-buffers":  uint(100),
		"sync":         false,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create SIP rtcp appsink: %w", err)
	}

	return &SipToWebrtc{
		log:    log,
		RtpBin: rtpBin,

		SipRtpSrc:     sipRtpSrc,
		H264Depay:     h264depay,
		H264Parse:     h264parse,
		AvdecH264:     avdecH264,
		VideoConvert:  videoconvert,
		VideoRate:     videorate,
		RateFilter:    ratefilter,
		VideoScale:    videoscale,
		ScaleFilter:   scalefilter,
		Queue:         queue,
		Vp8Enc:        vp8enc,
		RtpVp8Pay:     rtpVp8Pay,
		OutQueue:      outQueue,
		WebrtcRtpSink: webrtcRtpSink,

		SipRtcpSrc:  sipRtcpSrc,
		SipRtcpSink: sipRtcpSink,

		SipRtpAppSrc:     app.SrcFromElement(sipRtpSrc),
		WebrtcRtpAppSink: app.SinkFromElement(webrtcRtpSink),
		SipRtcpAppSrc:    app.SrcFromElement(sipRtcpSrc),
		SipRtcpAppSink:   app.SinkFromElement(sipRtcpSink),
	}, nil
}

// Add implements GstChain.
func (stw *SipToWebrtc) Add(p *gst.Pipeline) error {
	if err := p.AddMany(
		stw.RtpBin,
		stw.SipRtpSrc,
		stw.H264Depay,
		stw.H264Parse,
		stw.AvdecH264,
		stw.VideoConvert,
		stw.VideoRate,
		stw.RateFilter,
		stw.VideoScale,
		stw.ScaleFilter,
		stw.Queue,
		stw.Vp8Enc,
		stw.RtpVp8Pay,
		stw.OutQueue,
		stw.WebrtcRtpSink,
		stw.SipRtcpSrc,
		stw.SipRtcpSink,
	); err != nil {
		return fmt.Errorf("failed to add sip to webrtc chain to pipeline: %w", err)
	}
	return nil
}

// Link implements GstChain.
func (stw *SipToWebrtc) Link(p *gst.Pipeline) error {
	// link rtp path
	if err := pipeline.LinkPad(
		stw.SipRtpSrc.GetStaticPad("src"),
		stw.RtpBin.GetRequestPad("recv_rtp_sink_%u"),
	); err != nil {
		return fmt.Errorf("failed to link sip rtp src to rtpbin: %w", err)
	}

	if _, err := stw.RtpBin.Connect("pad-added", func(rtpbin *gst.Element, pad *gst.Pad) {
		padName := pad.GetName()
		if !strings.HasPrefix(padName, "recv_rtp_src_") {
			return
		}
		var sessionID, ssrc, payloadType uint32
		if _, err := fmt.Sscanf(padName, "recv_rtp_src_%d_%d_%d", &sessionID, &ssrc, &payloadType); err != nil {
			stw.log.Warnw("invalid RTP pad format", err, "pad", padName)
			return
		}
		stw.log.Debugw("rtpbin pad added", "ssrc", ssrc, "pt", payloadType)
		if err := pipeline.LinkPad(
			pad,
			stw.H264Depay.GetStaticPad("sink"),
		); err != nil {
			stw.log.Errorw("failed to link rtpbin to depayloader", err)
			return
		}
	}); err != nil {
		return fmt.Errorf("failed to connect to rtpbin pad-added signal: %w", err)
	}

	if err := gst.ElementLinkMany(
		stw.H264Depay,
		stw.H264Parse,
		stw.AvdecH264,
		stw.VideoConvert,
		stw.VideoRate,
		stw.RateFilter,
		stw.VideoScale,
		stw.ScaleFilter,
		stw.Queue,
		stw.Vp8Enc,
		stw.RtpVp8Pay,
		stw.OutQueue,
		stw.WebrtcRtpSink,
	); err != nil {
		return fmt.Errorf("failed to link sip to webrtc rtp path: %w", err)
	}

	// link rtcp path
	if err := pipeline.LinkPad(
		stw.SipRtcpSrc.GetStaticPad("src"),
		stw.RtpBin.GetRequestPad("recv_rtcp_sink_%u"),
	); err != nil {
		return fmt.Errorf("failed to link sip rtcp src to rtpbin: %w", err)
	}

	if err := pipeline.LinkPad(
		stw.RtpBin.GetRequestPad("send_rtcp_src_%u"),
		stw.SipRtcpSink.GetStaticPad("sink"),
	); err != nil {
		return fmt.Errorf("failed to link rtpbin rtcp src to sip rtcp sink: %w", err)
	}

	return nil
}

// Close implements GstChain.
func (stw *SipToWebrtc) Close(pipeline *gst.Pipeline) error {
	if err := pipeline.RemoveMany(
		stw.RtpBin,
		stw.SipRtpSrc,
		stw.H264Depay,
		stw.H264Parse,
		stw.AvdecH264,
		stw.VideoConvert,
		stw.VideoRate,
		stw.RateFilter,
		stw.VideoScale,
		stw.ScaleFilter,
		stw.Queue,
		stw.Vp8Enc,
		stw.RtpVp8Pay,
		stw.OutQueue,
		stw.WebrtcRtpSink,
		stw.SipRtcpSrc,
		stw.SipRtcpSink,
	); err != nil {
		return fmt.Errorf("failed to remove sip to webrtc chain from pipeline: %w", err)
	}
	return nil
}
