package camera_pipeline

import (
	"context"
	"fmt"
	"strings"

	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/sip/pkg/sip/pipeline"
	"github.com/livekit/sip/pkg/sip/pipeline/event"
)

func NewWebrtcIo(log logger.Logger, parent *CameraPipeline) *WebrtcIo {
	return &WebrtcIo{
		log:      log.WithComponent("webrtc_io"),
		pipeline: parent,
		Tracks:   make(map[uint32]*WebrtcTrack),
	}
}

type WebrtcIo struct {
	pipeline *CameraPipeline
	log      logger.Logger

	WebrtcRtpBin *gst.Element

	Tracks map[uint32]*WebrtcTrack

	RtpFunnel     *gst.Element
	InputSelector *gst.Element

	RtcpFunnel *gst.Element
	RtcpFilter *gst.Element

	WebrtcRtpOut  *gst.Element
	WebrtcRtcpOut *gst.Element
}

var _ pipeline.GstChain = (*WebrtcIo)(nil)

const VP8CAPS = "application/x-rtp,media=video,encoding-name=VP8,clock-rate=90000,payload=96,rtcp-fb-nack-pli=1,rtcp-fb-nack=1,rtcp-fb-ccm-fir=1"

var webrtcCaps = map[uint]string{
	96: VP8CAPS,
}

// Create implements [pipeline.GstChain].
func (wio *WebrtcIo) Create() error {
	var err error
	wio.WebrtcRtpBin, err = gst.NewElementWithProperties("rtpbin", map[string]interface{}{
		"name":        "webrtc_rtp_bin",
		"rtp-profile": int(3), // GST_RTP_PROFILE_AVPF
	})
	if err != nil {
		return fmt.Errorf("failed to create WebRTC rtpbin: %w", err)
	}

	// rtp
	wio.RtpFunnel, err = gst.NewElementWithProperties("rtpfunnel", map[string]interface{}{
		"name": "webrtc_rtp_funnel",
	})
	if err != nil {
		return fmt.Errorf("failed to create WebRTC rtp funnel: %w", err)
	}

	wio.InputSelector, err = gst.NewElementWithProperties("input-selector", map[string]interface{}{
		"name":          "webrtc_input_selector",
		"cache-buffers": true,
	})
	if err != nil {
		return fmt.Errorf("failed to create WebRTC input selector: %w", err)
	}

	// rtcp
	wio.RtcpFunnel, err = gst.NewElementWithProperties("funnel", map[string]interface{}{
		"name": "webrtc_rtcp_funnel",
	})
	if err != nil {
		return fmt.Errorf("failed to create WebRTC rtcp funnel: %w", err)
	}

	wio.RtcpFilter, err = gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"name": "webrtc_rtcp_filter",
		"caps": gst.NewCapsFromString("application/x-rtcp"),
	})
	if err != nil {
		return fmt.Errorf("failed to create WebRTC rtcp filter: %w", err)
	}

	wio.WebrtcRtpOut, err = gst.NewElementWithProperties("sinkwriter", map[string]interface{}{
		"name": "webrtc_rtp_out",
		// "caps":        gst.NewCapsFromString(VP8CAPS),
		"max-bitrate": int(1_500_000),
		"sync":        false,
		"async":       false,
	})
	if err != nil {
		return fmt.Errorf("failed to create WebRTC rtp sinkwriter: %w", err)
	}

	wio.WebrtcRtcpOut, err = gst.NewElementWithProperties("sinkwriter", map[string]interface{}{
		"name": "webrtc_rtcp_out",
		// "caps":  gst.NewCapsFromString("application/x-rtcp"),
		"sync":  false,
		"async": false,
	})
	if err != nil {
		return fmt.Errorf("failed to create WebRTC rtcp sinkwriter: %w", err)
	}

	return nil
}

// Add implements [pipeline.GstChain].
func (wio *WebrtcIo) Add() error {
	if err := wio.pipeline.Pipeline().AddMany(
		wio.WebrtcRtpBin,
		wio.RtpFunnel,
		wio.InputSelector,
		wio.RtcpFunnel,
		wio.RtcpFilter,
		wio.WebrtcRtpOut,
		wio.WebrtcRtcpOut,
	); err != nil {
		return fmt.Errorf("failed to add webrtc io to pipeline: %w", err)
	}
	return nil
}

// Link implements [pipeline.GstChain].
func (wio *WebrtcIo) Link() error {
	// pt map
	if _, err := wio.WebrtcRtpBin.Connect("request-pt-map", event.RegisterCallback(context.TODO(), wio.pipeline.Loop(), func(self *gst.Element, session uint, pt uint) *gst.Caps {
		caps, ok := webrtcCaps[pt]
		if !ok {
			return nil
		}
		wio.log.Debugw("RTPBIN requested PT map", "pt", pt, "caps", caps)
		return gst.NewCapsFromString(caps)
	})); err != nil {
		return fmt.Errorf("failed to connect to rtpbin request-pt-map signal: %w", err)
	}

	// link rtp in
	if _, err := wio.WebrtcRtpBin.Connect("pad-added", event.RegisterCallback(context.TODO(), wio.pipeline.Loop(), func(rtpbin *gst.Element, pad *gst.Pad) {
		wio.log.Debugw("WEBRTC RTPBIN PAD ADDED", "pad", pad.GetName())
		padName := pad.GetName()
		if !strings.HasPrefix(padName, "recv_rtp_src_0_") {
			return
		}
		var ssrc, payloadType uint32
		if _, err := fmt.Sscanf(padName, "recv_rtp_src_0_%d_%d", &ssrc, &payloadType); err != nil {
			wio.log.Warnw("Invalid RTP pad format", err, "pad", padName)
			return
		}
		wio.log.Infow("RTP pad added", "pad", padName, "ssrc", ssrc, "payloadType", payloadType)

		track, ok := wio.Tracks[ssrc]
		if !ok {
			wio.log.Warnw("No track found for RTP pad", nil, "ssrc", ssrc)
			return
		}

		if err := track.LinkParent(pad); err != nil {
			wio.log.Errorw("Failed to link track parent", err)
			return
		}
		wio.log.Infow("Linked RTP pad", "pad", padName)
	})); err != nil {
		return fmt.Errorf("failed to connect to rtpbin pad-added signal: %w", err)
	}

	if err := pipeline.LinkPad(
		wio.RtpFunnel.GetStaticPad("src"),
		wio.WebrtcRtpBin.GetRequestPad("recv_rtp_sink_0"),
	); err != nil {
		return fmt.Errorf("failed to link webrtc rtp src to rtpbin: %w", err)
	}

	if err := gst.ElementLinkMany(
		wio.InputSelector,
		wio.pipeline.WebrtcToSip.Vp8Dec,
	); err != nil {
		return fmt.Errorf("failed to link webrtc input selector to vp8 decoder: %w", err)
	}

	// link rtp out
	if _, err := wio.WebrtcRtpBin.Connect("pad-added", event.RegisterCallback(context.TODO(), wio.pipeline.Loop(), func(rtpbin *gst.Element, pad *gst.Pad) {
		wio.log.Debugw("WEBRTC RTPBIN PAD ADDED", "pad", pad.GetName())
		padName := pad.GetName()
		if padName != "send_rtp_src_0" {
			return
		}
		if err := pipeline.LinkPad(
			pad,
			wio.WebrtcRtpOut.GetStaticPad("sink"),
		); err != nil {
			wio.log.Errorw("Failed to link webrtc rtpbin pad to sinkwriter", err)
			return
		}
		wio.log.Infow("Linked WebRTC RTP pad", "pad", padName)
	})); err != nil {
		return fmt.Errorf("failed to connect to webrtc rtpbin pad-added signal: %w", err)
	}

	if err := pipeline.LinkPad(
		wio.pipeline.SipToWebrtc.CapsFilter.GetStaticPad("src"),
		wio.WebrtcRtpBin.GetRequestPad("send_rtp_sink_0"),
	); err != nil {
		return fmt.Errorf("failed to link rtp vp8 payloader to webrtc rtpbin: %w", err)
	}

	// link rtcp in
	if err := wio.RtcpFunnel.Link(wio.RtcpFilter); err != nil {
		return fmt.Errorf("failed to link webrtc rtcp funnel to filter: %w", err)
	}
	if err := pipeline.LinkPad(
		wio.RtcpFilter.GetStaticPad("src"),
		wio.WebrtcRtpBin.GetRequestPad("recv_rtcp_sink_0"),
	); err != nil {
		return fmt.Errorf("failed to link webrtc rtcp src to rtpbin: %w", err)
	}

	// link rtcp out
	if err := pipeline.LinkPad(
		wio.WebrtcRtpBin.GetRequestPad("send_rtcp_src_0"),
		wio.WebrtcRtcpOut.GetStaticPad("sink"),
	); err != nil {
		return fmt.Errorf("failed to link webrtc rtcpbin to sinkwriter: %w", err)
	}

	// configure rtpbin - session configuration is optional, don't fail if unavailable
	sess, err := wio.WebrtcRtpBin.Emit("get-internal-session", uint(0))
	if err != nil || sess == nil {
		wio.log.Warnw("could not get webrtc rtpbin internal session, RTCP feedback logging disabled", err, "sessNotNil", sess != nil)
	} else {
		sessElem := gst.ToElement(sess)
		if sessElem == nil || sessElem.Instance() == nil {
			wio.log.Warnw("could not cast webrtc rtpbin internal session to element, RTCP feedback logging disabled", nil)
		} else {
			if err := sessElem.SetProperty("rtcp-min-interval", uint64(0)); err != nil {
				wio.log.Warnw("failed to set webrtc rtpbin rtcp min interval", err)
			}
			if err := sessElem.SetProperty("rtcp-fraction", 0.10); err != nil {
				wio.log.Warnw("failed to set webrtc rtpbin rtcp fraction", err)
			}

			// Log RTCP feedback (PLI/FIR) requests from WebRTC peers
			if _, err := sessElem.Connect("on-feedback-rtcp", event.RegisterCallback(context.TODO(), wio.pipeline.Loop(), func(session *gst.Element, fbType uint, fbSubType uint, senderSsrc uint, mediaSsrc uint) {
				fbTypeName := "unknown"
				if fbType == 205 {
					fbTypeName = "RTPFB"
				} else if fbType == 206 {
					switch fbSubType {
					case 1:
						fbTypeName = "PLI"
					case 4:
						fbTypeName = "FIR"
					default:
						fbTypeName = fmt.Sprintf("PSFB-%d", fbSubType)
					}
				}
				wio.log.Infow("WebRTC peer sent RTCP feedback",
					"fbType", fbType,
					"fbSubType", fbSubType,
					"fbTypeName", fbTypeName,
					"senderSsrc", senderSsrc,
					"mediaSsrc", mediaSsrc)
			})); err != nil {
				wio.log.Warnw("failed to connect to webrtc rtpsession on-feedback-rtcp signal", err)
			}
		}
	}

	return nil
}

// Close implements [pipeline.GstChain].
func (wio *WebrtcIo) Close() error {
	var errs []error
	for _, track := range wio.Tracks {
		if err := track.Close(); err != nil {
			wio.log.Errorw("Failed to close webrtc track", err, "ssrc", track.SSRC)
			errs = append(errs, err)
		}
	}
	for k := range wio.Tracks {
		delete(wio.Tracks, k)
	}

	if err := wio.pipeline.Pipeline().RemoveMany(
		wio.WebrtcRtpBin,
		wio.RtpFunnel,
		wio.InputSelector,
		wio.RtcpFunnel,
		wio.RtcpFilter,
		wio.WebrtcRtpOut,
		wio.WebrtcRtcpOut,
	); err != nil {
		errs = append(errs, fmt.Errorf("failed to remove WebRTC IO elements from pipeline: %w", err))
	}

	if len(errs) > 0 {
		return fmt.Errorf("errors occurred while closing webrtc io: %v", errs)
	}

	return nil
}
