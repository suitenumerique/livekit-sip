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

func NewSipInput(log logger.Logger, parent *CameraPipeline) *SipIo {
	return &SipIo{
		log:      log.WithComponent("sip_input"),
		pipeline: parent,
	}
}

type SipIo struct {
	log      logger.Logger
	pipeline *CameraPipeline

	SipRtpBin *gst.Element

	SipRtpIn   *gst.Element
	SipRtcpIn  *gst.Element
	SipRtpOut  *gst.Element
	SipRtcpOut *gst.Element
}

var _ pipeline.GstChain = (*SipIo)(nil)

// Create implements [pipeline.GstChain].
func (sio *SipIo) Create() error {
	var err error
	sio.SipRtpBin, err = gst.NewElementWithProperties("rtpbin", map[string]interface{}{
		"name":        "sip_rtp_bin",
		"rtp-profile": int(3), // GST_RTP_PROFILE_AVPF
	})
	if err != nil {
		return fmt.Errorf("failed to create SIP rtpbin: %w", err)
	}

	sio.SipRtpIn, err = gst.NewElementWithProperties("sourcereader", map[string]interface{}{
		"name":         "sip_rtp_in",
		"do-timestamp": true,
	})
	if err != nil {
		return fmt.Errorf("failed to create SIP rtp sourcereader: %w", err)
	}

	sio.SipRtpOut, err = gst.NewElementWithProperties("sinkwriter", map[string]interface{}{
		"name":        "sip_rtp_out",
		"max-bitrate": int(1_500_000),
		"sync":        false,
		"async":       false,
	})
	if err != nil {
		return fmt.Errorf("failed to create SIP rtp sinkwriter: %w", err)
	}

	sio.SipRtcpIn, err = gst.NewElementWithProperties("sourcereader", map[string]interface{}{
		"name":         "sip_rtcp_in",
		"caps":         gst.NewCapsFromString("application/x-rtcp"),
		"do-timestamp": true,
	})
	if err != nil {
		return fmt.Errorf("failed to create SIP rtcp sourcereader: %w", err)
	}

	sio.SipRtcpOut, err = gst.NewElementWithProperties("sinkwriter", map[string]interface{}{
		"name":  "sip_rtcp_out",
		"caps":  gst.NewCapsFromString("application/x-rtcp"),
		"sync":  false,
		"async": false,
	})
	if err != nil {
		return fmt.Errorf("failed to create SIP rtcp sinkwriter: %w", err)
	}

	return nil
}

// Add implements [pipeline.GstChain].
func (sio *SipIo) Add() error {
	return sio.pipeline.Pipeline().AddMany(
		sio.SipRtpBin,
		sio.SipRtpIn,
		sio.SipRtcpIn,
		sio.SipRtpOut,
		sio.SipRtcpOut,
	)
}

// Link implements [pipeline.GstChain].
func (sio *SipIo) Link() error {
	// link rtp in
	if _, err := sio.SipRtpBin.Connect("pad-added", event.RegisterCallback(context.TODO(), sio.pipeline.Loop(), func(rtpbin *gst.Element, pad *gst.Pad) {
		sio.log.Debugw("RTPBIN PAD ADDED", "pad", pad.GetName())
		padName := pad.GetName()
		if !strings.HasPrefix(padName, "recv_rtp_src_0_") {
			return
		}
		var ssrc, payloadType uint32
		if _, err := fmt.Sscanf(padName, "recv_rtp_src_0_%d_%d", &ssrc, &payloadType); err != nil {
			sio.log.Warnw("Invalid RTP pad format", err, "pad", padName)
			return
		}
		sio.log.Infow("RTP pad added", "pad", padName, "ssrc", ssrc, "payloadType", payloadType)
		if err := pipeline.LinkPad(
			pad,
			sio.pipeline.SipToWebrtc.H264Depay.GetStaticPad("sink"),
		); err != nil {
			sio.log.Errorw("Failed to link rtpbin pad to depayloader", err)
			return
		}
		sio.log.Infow("Linked RTP pad", "pad", padName)
	})); err != nil {
		return fmt.Errorf("failed to connect to rtpbin pad-added signal: %w", err)
	}

	if err := pipeline.LinkPad(
		sio.SipRtpIn.GetStaticPad("src"),
		sio.SipRtpBin.GetRequestPad("recv_rtp_sink_0"),
	); err != nil {
		return fmt.Errorf("failed to link sip rtp src to rtpbin: %w", err)
	}

	// link rtp out
	if _, err := sio.SipRtpBin.Connect("pad-added", event.RegisterCallback(context.TODO(), sio.pipeline.Loop(), func(rtpbin *gst.Element, pad *gst.Pad) {
		sio.log.Debugw("WEBRTC RTPBIN PAD ADDED", "pad", pad.GetName())
		padName := pad.GetName()
		if padName != "send_rtp_src_0" {
			return
		}
		if err := pipeline.LinkPad(
			pad,
			sio.SipRtpOut.GetStaticPad("sink"),
		); err != nil {
			sio.log.Errorw("Failed to link sip rtpbin pad to sinkwriter", err)
			return
		}
		sio.log.Infow("Linked SIP RTP pad", "pad", padName)
	})); err != nil {
		return fmt.Errorf("failed to connect to sip rtpbin pad-added signal: %w", err)
	}

	if err := pipeline.LinkPad(
		sio.pipeline.WebrtcToSip.CapsFilter.GetStaticPad("src"),
		sio.SipRtpBin.GetRequestPad("send_rtp_sink_0"),
	); err != nil {
		return fmt.Errorf("failed to link rtp vp8 payloader to sip rtpbin: %w", err)
	}

	// link rtcp in
	if err := pipeline.LinkPad(
		sio.SipRtcpIn.GetStaticPad("src"),
		sio.SipRtpBin.GetRequestPad("recv_rtcp_sink_0"),
	); err != nil {
		return fmt.Errorf("failed to link sip rtcp src to rtpbin: %w", err)
	}

	if err := pipeline.LinkPad(
		sio.SipRtpBin.GetRequestPad("send_rtcp_src_0"),
		sio.SipRtcpOut.GetStaticPad("sink"),
	); err != nil {
		return fmt.Errorf("failed to link rtpbin rtcp src to sip rtcp sink: %w", err)
	}

	// configure rtpbin - session configuration is optional, don't fail if unavailable
	sess, err := sio.SipRtpBin.Emit("get-internal-session", uint(0))
	if err != nil || sess == nil {
		sio.log.Warnw("could not get sip rtpbin internal session, RTCP feedback logging disabled", err, "sessNotNil", sess != nil)
	} else {
		sessElem := gst.ToElement(sess)
		if sessElem == nil || sessElem.Instance() == nil {
			sio.log.Warnw("could not cast sip rtpbin internal session to element, RTCP feedback logging disabled", nil)
		} else {
			if err := sessElem.SetProperty("rtcp-min-interval", uint64(0)); err != nil {
				sio.log.Warnw("failed to set sip rtpbin rtcp min interval", err)
			}
			if err := sessElem.SetProperty("rtcp-fraction", 0.10); err != nil {
				sio.log.Warnw("failed to set sip rtpbin rtcp fraction", err)
			}

			// Log RTCP feedback (PLI/FIR) requests from SIP device
			if _, err := sessElem.Connect("on-feedback-rtcp", event.RegisterCallback(context.TODO(), sio.pipeline.Loop(), func(session *gst.Element, fbType uint, fbSubType uint, senderSsrc uint, mediaSsrc uint) {
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
				sio.log.Infow("SIP device sent RTCP feedback",
					"fbType", fbType,
					"fbSubType", fbSubType,
					"fbTypeName", fbTypeName,
					"senderSsrc", senderSsrc,
					"mediaSsrc", mediaSsrc)
			})); err != nil {
				sio.log.Warnw("failed to connect to rtpsession on-feedback-rtcp signal", err)
			}
		}
	}

	return nil
}

// Close implements [pipeline.GstChain].
func (sio *SipIo) Close() error {
	if err := sio.pipeline.Pipeline().RemoveMany(
		sio.SipRtpBin,
		sio.SipRtpIn,
		sio.SipRtcpIn,
		sio.SipRtpOut,
		sio.SipRtcpOut,
	); err != nil {
		return fmt.Errorf("failed to remove SIP IO elements from pipeline: %w", err)
	}
	return nil
}
