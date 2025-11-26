package pipeline

import (
	"fmt"
	"strings"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/app"
	"github.com/livekit/protocol/logger"
)

type SipToWebrtc struct {
	log logger.Logger

	RtpBin *gst.Element

	SipRtpSrc *gst.Element
	// rptbin
	Depay         *gst.Element
	Parse         *gst.Element
	Decoder       *gst.Element
	VideoConvert  *gst.Element
	VideoScale    *gst.Element
	ResFilter     *gst.Element
	VideoConvert2 *gst.Element
	VideoRate     *gst.Element
	FpsFilter     *gst.Element
	Vp8Enc        *gst.Element
	RtpVp8Pay     *gst.Element
	WebrtcRtpSink *gst.Element

	SipRtcpSrc *gst.Element
	// rptbin
	SipRtcpSink *gst.Element

	SipRtpAppSrc     *app.Source
	WebrtcRtpAppSink *app.Sink
	SipRtcpAppSrc    *app.Source
	SipRtcpAppSink   *app.Sink
}

var _ GstChain = (*SipToWebrtc)(nil)

func buildSipToWebRTCChain(log logger.Logger, sipPayloadType int) (*SipToWebrtc, error) {
	rtpBin, err := gst.NewElementWithProperties("rtpbin", map[string]interface{}{
		"name": "sip_rtp_bin",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create SIP rtpbin: %w", err)
	}

	sipRtpSrc, err := gst.NewElementWithProperties("appsrc", map[string]interface{}{
		"name":         "sip_rtp_in",
		"is-live":      true,
		"do-timestamp": true,
		"format":       int(3), // GST_FORMAT_TIME; using the same numeric value as your launch string
		"max-bytes":    uint64(5_000_000),
		"block":        false,
		"caps": gst.NewCapsFromString(fmt.Sprintf(
			"application/x-rtp,media=video,encoding-name=H264,payload=%d,clock-rate=90000",
			sipPayloadType,
		)),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create SIP appsrc: %w", err)
	}

	depay, err := gst.NewElementWithProperties("rtph264depay", map[string]interface{}{
		"request-keyframe": true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create SIP rtp depayloader: %w", err)
	}

	parse, err := gst.NewElementWithProperties("h264parse", map[string]interface{}{
		"config-interval": int(1),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create SIP h264 parser: %w", err)
	}

	dec, err := gst.NewElementWithProperties("avdec_h264", map[string]interface{}{
		"max-threads": int(4),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create SIP h264 decoder: %w", err)
	}

	vconv, err := gst.NewElement("videoconvert")
	if err != nil {
		return nil, fmt.Errorf("failed to create SIP videoconvert: %w", err)
	}

	vscale, err := gst.NewElementWithProperties("videoscale", map[string]interface{}{
		"add-borders": true, // Add black bars for aspect ratio preservation
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create SIP videoscale: %w", err)
	}

	// Force 1280x720 resolution with PAR 1:1 - this forces letterboxing for non-16:9 content
	resFilter, err := gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString("video/x-raw,width=1280,height=720,pixel-aspect-ratio=1/1"),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create SIP resolution capsfilter: %w", err)
	}

	// videoconvert after scaling to ensure proper format
	vconv2, err := gst.NewElement("videoconvert")
	if err != nil {
		return nil, fmt.Errorf("failed to create SIP videoconvert2: %w", err)
	}

	// Force 24fps output
	vrate, err := gst.NewElementWithProperties("videorate", map[string]interface{}{
		"drop-only": true, // Only drop frames, don't duplicate
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create SIP videorate: %w", err)
	}

	// Force 24fps in caps
	fpsFilter, err := gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString("video/x-raw,framerate=24/1"),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create SIP fps capsfilter: %w", err)
	}

	vp8enc, err := gst.NewElementWithProperties("vp8enc", map[string]interface{}{
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
		Depay:         depay,
		Parse:         parse,
		Decoder:       dec,
		VideoConvert:  vconv,
		VideoScale:    vscale,
		ResFilter:     resFilter,
		VideoConvert2: vconv2,
		VideoRate:     vrate,
		FpsFilter:     fpsFilter,
		Vp8Enc:        vp8enc,
		RtpVp8Pay:     rtpVp8Pay,
		WebrtcRtpSink: webrtcRtpSink,

		SipRtcpSink: sipRtcpSink,
		SipRtcpSrc:  sipRtcpSrc,

		SipRtpAppSrc:     app.SrcFromElement(sipRtpSrc),
		WebrtcRtpAppSink: app.SinkFromElement(webrtcRtpSink),
		SipRtcpAppSrc:    app.SrcFromElement(sipRtcpSrc),
		SipRtcpAppSink:   app.SinkFromElement(sipRtcpSink),
	}, nil
}

// Add implements GstChain.
func (stw *SipToWebrtc) Add(pipeline *gst.Pipeline) error {
	if err := pipeline.AddMany(
		stw.RtpBin,

		stw.SipRtpSrc,
		stw.Depay,
		stw.Parse,
		stw.Decoder,
		stw.VideoConvert,
		stw.VideoScale,
		stw.ResFilter,
		stw.VideoConvert2,
		stw.VideoRate,
		stw.FpsFilter,
		stw.Vp8Enc,
		stw.RtpVp8Pay,
		stw.WebrtcRtpSink,

		stw.SipRtcpSrc,
		stw.SipRtcpSink,
	); err != nil {
		return fmt.Errorf("failed to add sip to webrtc chain to pipeline: %w", err)
	}
	return nil
}

// Link implements GstChain.
func (stw *SipToWebrtc) Link(pipeline *gst.Pipeline) error {
	// link rtp path
	if err := linkPad(
		stw.SipRtpSrc.GetStaticPad("src"),
		stw.RtpBin.GetRequestPad("recv_rtp_sink_%u"),
	); err != nil {
		return fmt.Errorf("failed to link sip rtp src to rtpbin: %w", err)
	}

	var (
		hnd glib.SignalHandle
		err error
	)
	if hnd, err = stw.RtpBin.Connect("pad-added", func(rtpbin *gst.Element, pad *gst.Pad) {
		stw.log.Debugw("RTPBIN PAD ADDED", "pad", pad.GetName())
		padName := pad.GetName()
		if !strings.HasPrefix(padName, "recv_rtp_src_") {
			return
		}
		var sessionID, ssrc, payloadType uint32
		if _, err := fmt.Sscanf(padName, "recv_rtp_src_%d_%d_%d", &sessionID, &ssrc, &payloadType); err != nil {
			stw.log.Warnw("Invalid RTP pad format", err, "pad", padName)
			return
		}
		stw.log.Infow("RTP pad added", "pad", padName, "sessionID", sessionID, "ssrc", ssrc, "payloadType", payloadType)
		if err := linkPad(
			pad,
			stw.Depay.GetStaticPad("sink"),
		); err != nil {
			stw.log.Errorw("Failed to link rtpbin pad to depayloader", err)
			return
		}
		stw.log.Infow("Linked RTP pad", "pad", padName)
		stw.RtpBin.HandlerDisconnect(hnd)
	}); err != nil {
		return fmt.Errorf("failed to connect to rtpbin pad-added signal: %w", err)
	}

	if err := gst.ElementLinkMany(
		stw.Depay,
		stw.Parse,
		stw.Decoder,
		stw.VideoConvert,
		stw.VideoScale,
		stw.ResFilter,
		stw.VideoConvert2,
		stw.VideoRate,
		stw.FpsFilter,
		stw.Vp8Enc,
		stw.RtpVp8Pay,
		stw.WebrtcRtpSink,
	); err != nil {
		return fmt.Errorf("failed to link sip to webrtc rtp path: %w", err)
	}

	// link rtcp path
	if err := linkPad(
		stw.SipRtcpSrc.GetStaticPad("src"),
		stw.RtpBin.GetRequestPad("recv_rtcp_sink_%u"),
	); err != nil {
		return fmt.Errorf("failed to link sip rtcp src to rtpbin: %w", err)
	}

	if err := linkPad(
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
		stw.Depay,
		stw.Parse,
		stw.Decoder,
		stw.VideoConvert,
		stw.VideoScale,
		stw.ResFilter,
		stw.VideoConvert2,
		stw.VideoRate,
		stw.FpsFilter,
		stw.Vp8Enc,
		stw.RtpVp8Pay,
		stw.WebrtcRtpSink,

		stw.SipRtcpSink,
		stw.SipRtcpSrc,
	); err != nil {
		return fmt.Errorf("failed to remove sip to webrtc chain from pipeline: %w", err)
	}
	return nil
}
