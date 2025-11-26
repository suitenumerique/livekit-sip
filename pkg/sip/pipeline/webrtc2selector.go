package pipeline

import (
	"fmt"
	"strings"

	"github.com/frostbyte73/core"
	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/app"
	"github.com/livekit/protocol/logger"
)

type WebrtcToSelector struct {
	log logger.Logger

	linked core.Fuse
	SSRC   uint32
	parent *SelectorToSip

	RtpBin *gst.Element

	WebrtcRtpSrc *gst.Element
	// rptbin
	RtpVp8Depay *gst.Element
	RtpQueue    *gst.Element

	WebrtcRtcpSrc *gst.Element
	// rptbin
	RtcpQueue *gst.Element

	WebrtcRtpAppSrc  *app.Source
	WebrtcRtcpAppSrc *app.Source
	RtpPad           *gst.Pad
	RtcpPad          *gst.Pad
}

var _ GstChain = (*WebrtcToSelector)(nil)

func buildWebRTCToSelectorChain(log logger.Logger, parent *SelectorToSip, sid string, ssrc uint32) (*WebrtcToSelector, error) {
	log = log.WithValues("sid", sid, "ssrc", ssrc)

	rtpBin, err := gst.NewElementWithProperties("rtpbin", map[string]interface{}{
		"autoremove":      true,
		"do-lost":         true,
		"do-sync-event":   true,
		"drop-on-latency": true,
		"latency":         uint64(50),
		// "ignore-pt":          true,
		"rtcp-sync-interval": uint64(1000000000), // 1s
		"rtp-profile":        int(3),             // RTP_PROFILE_AVPF
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create SIP rtpbin: %w", err)
	}

	webrtcRtpSrc, err := gst.NewElementWithProperties("appsrc", map[string]interface{}{
		"name":         fmt.Sprintf("webrtc_rtp_in_%s", sid),
		"is-live":      true,
		"do-timestamp": true,
		"format":       int(gst.FormatTime),
		"max-bytes":    uint64(2_000_000),
		"block":        false,
		"caps": gst.NewCapsFromString(
			"application/x-rtp,media=video,encoding-name=VP8,clock-rate=90000,payload=96,rtcp-fb-nack-pli=true"),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create WebRTC RTP appsrc: %w", err)
	}

	depay, err := gst.NewElementWithProperties("rtpvp8depay", map[string]interface{}{
		"request-keyframe": true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create WebRTC RTP depayloader: %w", err)
	}

	RtpQueue, err := gst.NewElement("queue")
	if err != nil {
		return nil, fmt.Errorf("failed to create WebRTC RTP queue: %w", err)
	}

	webrtcRtcpSrc, err := gst.NewElementWithProperties("appsrc", map[string]interface{}{
		"name":         fmt.Sprintf("webrtc_rtcp_in_%s", sid),
		"is-live":      true,
		"do-timestamp": true,
		"format":       int(gst.FormatTime),
		"max-bytes":    uint64(500_000),
		"block":        false,
		"caps":         gst.NewCapsFromString("application/x-rtcp"),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create WebRTC RTCP appsrc: %w", err)
	}

	RtcpQueue, err := gst.NewElement("queue")
	if err != nil {
		return nil, fmt.Errorf("failed to create WebRTC RTCP queue: %w", err)
	}

	return &WebrtcToSelector{
		log:    log,
		parent: parent,
		SSRC:   ssrc,
		RtpBin: rtpBin,

		WebrtcRtpSrc: webrtcRtpSrc,
		RtpVp8Depay:  depay,
		RtpQueue:     RtpQueue,

		WebrtcRtcpSrc: webrtcRtcpSrc,
		RtcpQueue:     RtcpQueue,

		WebrtcRtpAppSrc:  app.SrcFromElement(webrtcRtpSrc),
		WebrtcRtcpAppSrc: app.SrcFromElement(webrtcRtcpSrc),
	}, nil
}

func (wts *WebrtcToSelector) sync() error {
	for _, elem := range []*gst.Element{
		wts.WebrtcRtpSrc,
		wts.WebrtcRtcpSrc,

		wts.RtpBin,

		wts.RtpVp8Depay,
		wts.RtpQueue,

		wts.RtcpQueue,
	} {
		if ok := elem.SyncStateWithParent(); !ok {
			return fmt.Errorf("failed to sync state for %s", elem.GetName())
		}
	}
	return nil
}

// Add implements GstChain.
func (wts *WebrtcToSelector) Add(pipeline *gst.Pipeline) error {
	if err := pipeline.AddMany(
		wts.RtpBin,

		wts.WebrtcRtpSrc,
		wts.RtpVp8Depay,
		wts.RtpQueue,

		wts.WebrtcRtcpSrc,
		wts.RtcpQueue,
	); err != nil {
		return fmt.Errorf("failed to add elements to pipeline: %w", err)
	}

	return nil
}

// Link implements GstChain.
func (wts *WebrtcToSelector) Link(pipeline *gst.Pipeline) error {
	if !wts.linked.Break() {
		return nil
	}

	// Link RTP path
	if err := linkPad(
		wts.WebrtcRtpSrc.GetStaticPad("src"),
		wts.RtpBin.GetRequestPad("recv_rtp_sink_%u"),
	); err != nil {
		return fmt.Errorf("failed to link webrtc rtp src to rtpbin: %w", err)
	}

	var (
		err       error
		hnd       glib.SignalHandle
		connected bool
	)
	if hnd, err = wts.RtpBin.Connect("pad-added", func(rtpbin *gst.Element, pad *gst.Pad) {
		wts.log.Debugw("RTPBIN PAD ADDED", "pad", pad.GetName())
		if connected {
			return
		}
		padName := pad.GetName()
		if !strings.HasPrefix(padName, "recv_rtp_src_") {
			return
		}
		var sessionID, ssrc, payloadType uint32
		if _, err := fmt.Sscanf(padName, "recv_rtp_src_%d_%d_%d", &sessionID, &ssrc, &payloadType); err != nil {
			wts.log.Warnw("Invalid RTP pad format", err, "pad", padName)
			return
		}
		wts.log.Infow("RTP pad added", "pad", padName, "sessionID", sessionID, "ssrc", ssrc, "payloadType", payloadType)
		if err := linkPad(
			pad,
			wts.RtpVp8Depay.GetStaticPad("sink"),
		); err != nil {
			wts.log.Errorw("Failed to link rtpbin pad to depayloader", err)
			return
		}
		wts.log.Infow("Linked RTP pad", "pad", padName)
		connected = true
		wts.RtpBin.HandlerDisconnect(hnd)
	}); err != nil {
		return fmt.Errorf("failed to connect to rtpbin pad-added signal: %w", err)
	}

	if err := gst.ElementLinkMany(
		wts.RtpVp8Depay,
		wts.RtpQueue,
	); err != nil {
		return fmt.Errorf("failed to link webrtc to selector rtp path: %w", err)
	}

	wts.RtpPad = wts.parent.RtpInputSelector.GetRequestPad("sink_%u")
	if err := linkPad(
		wts.RtpQueue.GetStaticPad("src"),
		wts.RtpPad,
	); err != nil {
		return fmt.Errorf("failed to link webrtc rtp queue to selector: %w", err)
	}

	// link rtcp path
	if err := linkPad(
		wts.WebrtcRtcpSrc.GetStaticPad("src"),
		wts.RtpBin.GetRequestPad("recv_rtcp_sink_%u"),
	); err != nil {
		return fmt.Errorf("failed to link webrtc rtcp src to rtpbin: %w", err)
	}

	if err := linkPad(
		wts.RtpBin.GetRequestPad("send_rtcp_src_%u"),
		wts.RtcpQueue.GetStaticPad("sink"),
	); err != nil {
		return fmt.Errorf("failed to link rtpbin rtcp src to webrtc rtcp queue: %w", err)
	}

	wts.RtcpPad = wts.parent.RtcpFunnel.GetRequestPad("sink_%u")
	if err := linkPad(
		wts.RtcpQueue.GetStaticPad("src"),
		wts.RtcpPad,
	); err != nil {
		return fmt.Errorf("failed to link webrtc rtcp queue to funnel: %w", err)
	}

	return wts.sync()
}

// Close implements GstChain.
func (wts *WebrtcToSelector) Close(pipeline *gst.Pipeline) error {

	releasePad(wts.RtpPad)
	releasePad(wts.RtcpPad)

	if err := pipeline.RemoveMany(
		wts.RtpBin,

		wts.WebrtcRtpSrc,
		wts.RtpVp8Depay,
		wts.RtpQueue,

		wts.WebrtcRtcpSrc,
		wts.RtcpQueue,
	); err != nil {
		return fmt.Errorf("failed to remove elements from pipeline: %w", err)
	}

	return nil
}
