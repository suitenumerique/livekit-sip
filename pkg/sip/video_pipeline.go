package sip

import (
	"fmt"
	"io"

	sdpv2 "github.com/livekit/media-sdk/sdp/v2"
	"github.com/livekit/sip/pkg/sip/pipeline"
)

func (v *VideoManager) Copy(dst io.WriteCloser, src io.ReadCloser) {
	n, err := io.Copy(dst, src)
	v.log.Infow("finished copying video data", "bytes", n, "err", err)
	src.Close()
	dst.Close()
}

func (v *VideoManager) SetupGstPipeline(media *sdpv2.SDPMedia) error {
	pipeline, err := pipeline.NewGstPipeline(v.log, int(media.Codec.PayloadType), int(media.Codec.PayloadType))
	if err != nil {
		return fmt.Errorf("failed to create SIP WebRTC pipeline: %w", err)
	}
	pipeline.Monitor()

	// setup SIP to WebRTC pipeline
	// link rtp path
	sipRtpIn, err := NewGstWriter(pipeline.SipRtpAppSrc)
	if err != nil {
		return fmt.Errorf("failed to create SIP RTP reader: %w", err)
	}
	go v.Copy(sipRtpIn, v.sipRtpIn)

	webrtcRtpOut, err := NewGstReader(pipeline.WebrtcRtpAppSink)
	if err != nil {
		return fmt.Errorf("failed to create WebRTC RTP writer: %w", err)
	}
	go v.Copy(v.webrtcRtpOut, webrtcRtpOut)

	// link rtcp path
	sipRtcpIn, err := NewGstWriter(pipeline.SipRtcpAppSrc)
	if err != nil {
		return fmt.Errorf("failed to create SIP RTCP reader: %w", err)
	}
	go v.Copy(sipRtcpIn, v.sipRtcpIn)

	sipRtcpOut, err := NewGstReader(pipeline.SipRtcpAppSink)
	if err != nil {
		return fmt.Errorf("failed to create WebRTC RTCP writer: %w", err)
	}
	go v.Copy(v.sipRtcpOut, sipRtcpOut)

	// setup WebRTC to SIP pipeline
	// link rtp path
	sipRtpOut, err := NewGstReader(pipeline.SipRtpAppSink)
	if err != nil {
		return fmt.Errorf("failed to create SIP RTP writer: %w", err)
	}
	go v.Copy(v.sipRtpOut, sipRtpOut)

	// link rtcp path
	webrtcRtcpOut, err := NewGstReader(pipeline.WebrtcRtcpAppSink)
	if err != nil {
		return fmt.Errorf("failed to create WebRTC RTP writer: %w", err)
	}
	go v.Copy(v.webrtcRtcpOut, webrtcRtcpOut)

	v.pipeline = pipeline

	return nil
}
