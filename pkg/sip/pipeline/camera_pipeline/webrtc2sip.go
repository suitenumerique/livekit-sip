package camera_pipeline

import (
	"fmt"
	"strings"
	"sync"
	"time"

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
	log          logger.Logger
	mu           sync.Mutex
	WebrtcTracks map[uint32]*WebrtcTrack

	RtpBin *gst.Element

	RtpFunnel     *gst.Element
	InputSelector *gst.Element
	Vp8Dec        *gst.Element
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

	RtcpFunnel     *gst.Element
	WebrtcRtcpSink *gst.Element

	SipRtpAppSink     *app.Sink
	WebrtcRtcpAppSink *app.Sink
}

var _ pipeline.GstChain = (*WebrtcToSip)(nil)

func buildSelectorToSipChain(log logger.Logger, sipOutPayloadType int) (*WebrtcToSip, error) {
	rtpbin, err := gst.NewElementWithProperties("rtpbin", map[string]interface{}{
		"autoremove":         true,
		"do-lost":            true,
		"do-sync-event":      true,
		"drop-on-latency":    true,
		"latency":            uint64(0),
		"rtcp-sync-interval": uint64(1000000000), // 1s
		"rtp-profile":        int(3),             // RTP_PROFILE_AVPF
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc rtpbin: %w", err)
	}

	rtpfunnel, err := gst.NewElementWithProperties("rtpfunnel", map[string]interface{}{})
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc rtp funnel: %w", err)
	}

	inputSelector, err := gst.NewElementWithProperties("input-selector", map[string]interface{}{})
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc input selector: %w", err)
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

	rtcpFunnel, err := gst.NewElementWithProperties("funnel", map[string]interface{}{})
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc rtcp funnel: %w", err)
	}

	outQueue, err := gst.NewElementWithProperties("queue", map[string]interface{}{
		"max-size-time": uint64(100000000), // 100ms
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc rtcp out queue: %w", err)
	}

	webrtcRtcpSink, err := gst.NewElementWithProperties("appsink", map[string]interface{}{
		"name":         "webrtc_rtcp_out",
		"emit-signals": false,
		"drop":         false,
		"max-buffers":  uint(100),
		"sync":         false,
		"async":        false, // Don't wait for preroll - live pipeline
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc rtcp appsink: %w", err)
	}

	return &WebrtcToSip{
		log:          log,
		WebrtcTracks: make(map[uint32]*WebrtcTrack),

		RtpBin: rtpbin,

		RtpFunnel:     rtpfunnel,
		InputSelector: inputSelector,
		Vp8Dec:        vp8dec,
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

		RtcpFunnel:     rtcpFunnel,
		WebrtcRtcpSink: webrtcRtcpSink,

		SipRtpAppSink:     app.SinkFromElement(sipRtpSink),
		WebrtcRtcpAppSink: app.SinkFromElement(webrtcRtcpSink),
	}, nil
}

// Add implements GstChain.
func (wts *WebrtcToSip) Add(pipeline *gst.Pipeline) error {
	if err := pipeline.AddMany(
		wts.RtpBin,
		wts.RtpFunnel,
		wts.InputSelector,
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
		wts.RtcpFunnel,
		wts.WebrtcRtcpSink,
	); err != nil {
		return fmt.Errorf("failed to add SelectorToSip elements to pipeline: %w", err)
	}
	return nil
}

// Link implements GstChain.
func (wts *WebrtcToSip) Link(p *gst.Pipeline) error {
	if _, err := wts.RtpBin.Connect("request-pt-map", func(self *gst.Element, session uint, pt uint) *gst.Caps {
		caps, ok := webrtcCaps[pt]
		if !ok {
			return nil
		}
		wts.log.Debugw("rtpbin pt-map", "pt", pt)
		return gst.NewCapsFromString(caps)
	}); err != nil {
		return fmt.Errorf("failed to connect to rtpbin request-pt-map signal: %w", err)
	}

	if err := pipeline.LinkPad(
		wts.RtpFunnel.GetStaticPad("src"),
		wts.RtpBin.GetRequestPad("recv_rtp_sink_0"),
	); err != nil {
		return fmt.Errorf("failed to link WebrtcToSip rtp funnel to rtpbin: %w", err)
	}

	if _, err := wts.RtpBin.Connect("pad-added", func(rtpbin *gst.Element, pad *gst.Pad) {
		padName := pad.GetName()
		if !strings.HasPrefix(padName, "recv_rtp_src_") {
			return
		}
		var sessionID, ssrc, payloadType uint32
		if _, err := fmt.Sscanf(padName, "recv_rtp_src_%d_%d_%d", &sessionID, &ssrc, &payloadType); err != nil {
			wts.log.Warnw("invalid RTP pad format", err, "pad", padName)
			return
		}
		wts.log.Debugw("rtpbin pad added", "ssrc", ssrc, "pt", payloadType)

		wts.mu.Lock()
		defer wts.mu.Unlock()

		track, ok := wts.WebrtcTracks[ssrc]
		if !ok {
			wts.log.Warnw("no track for SSRC", nil, "ssrc", ssrc)
			return
		}

		if err := track.LinkParent(wts, pad); err != nil {
			wts.log.Errorw("failed to link pad to track", err, "ssrc", ssrc)
			return
		}
	}); err != nil {
		return fmt.Errorf("failed to connect to rtpbin pad-added signal: %w", err)
	}

	if err := gst.ElementLinkMany(
		wts.InputSelector,
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
		return fmt.Errorf("failed to link SelectorToSip video elements: %w", err)
	}

	if err := pipeline.LinkPad(
		wts.RtcpFunnel.GetStaticPad("src"),
		wts.RtpBin.GetRequestPad("recv_rtcp_sink_0"),
	); err != nil {
		return fmt.Errorf("failed to link SelectorToSip rtcp funnel to rtpbin: %w", err)
	}

	if err := pipeline.LinkPad(
		wts.RtpBin.GetRequestPad("send_rtcp_src_0"),
		wts.WebrtcRtcpSink.GetStaticPad("sink"),
	); err != nil {
		return fmt.Errorf("failed to link SelectorToSip rtcp rtpbin to webrtc rtcp sink: %w", err)
	}

	return nil
}

// Close implements GstChain.
func (wts *WebrtcToSip) Close(pipeline *gst.Pipeline) error {
	var errs []error
	for _, track := range wts.WebrtcTracks {
		if err := track.Close(pipeline); err != nil {
			errs = append(errs, fmt.Errorf("failed to close webrtc to selector: %w", err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("errors closing SelectorToSip: %v", errs)
	}

	time.Sleep(100 * time.Millisecond) // webrtc tracks need time to unlink

	pipeline.RemoveMany(
		wts.RtpBin,
		wts.RtpFunnel,
		wts.InputSelector,
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
		wts.RtcpFunnel,
		wts.WebrtcRtcpSink,
	)
	return nil
}
