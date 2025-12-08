package sip

import (
	"fmt"

	sdpv2 "github.com/livekit/media-sdk/sdp/v2"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/sip/pkg/sip/pipeline"
	"github.com/livekit/sip/pkg/sip/pipeline/camera_pipeline"
)

type CameraManager struct {
	*VideoManager
	ssrcs map[string]uint32
}

func NewCameraManager(log logger.Logger, room *Room, opts *MediaOptions) (*CameraManager, error) {
	cm := &CameraManager{
		ssrcs: make(map[string]uint32),
	}

	vm, err := NewVideoManager(log, room, opts, cm)
	if err != nil {
		return nil, err
	}
	cm.VideoManager = vm

	return cm, nil
}

func (cm *CameraManager) NewPipeline(media *sdpv2.SDPMedia) (pipeline.GspPipeline, error) {
	pipeline, err := camera_pipeline.New(cm.log, media.Codec.PayloadType)
	if err != nil {
		return nil, fmt.Errorf("failed to create SIP WebRTC pipeline: %w", err)
	}
	// pipeline.Monitor()

	// setup SIP to WebRTC pipeline
	// link rtp path
	sipRtpIn, err := NewGstWriter(pipeline.SipToWebrtc.SipRtpAppSrc)
	if err != nil {
		return nil, fmt.Errorf("failed to create SIP RTP reader: %w", err)
	}
	go cm.Copy(sipRtpIn, cm.sipRtpIn)

	webrtcRtpOut, err := NewGstReader(pipeline.SipToWebrtc.WebrtcRtpAppSink)
	if err != nil {
		return nil, fmt.Errorf("failed to create WebRTC RTP writer: %w", err)
	}
	go cm.Copy(cm.webrtcRtpOut, webrtcRtpOut)

	// link rtcp path
	sipRtcpIn, err := NewGstWriter(pipeline.SipToWebrtc.SipRtcpAppSrc)
	if err != nil {
		return nil, fmt.Errorf("failed to create SIP RTCP reader: %w", err)
	}
	go cm.Copy(sipRtcpIn, cm.sipRtcpIn)

	sipRtcpOut, err := NewGstReader(pipeline.SipToWebrtc.SipRtcpAppSink)
	if err != nil {
		return nil, fmt.Errorf("failed to create WebRTC RTCP writer: %w", err)
	}
	go cm.Copy(cm.sipRtcpOut, sipRtcpOut)

	// setup WebRTC to SIP pipeline
	// link rtp path
	sipRtpOut, err := NewGstReader(pipeline.WebrtcToSip.SipRtpAppSink)
	if err != nil {
		return nil, fmt.Errorf("failed to create SIP RTP writer: %w", err)
	}
	go cm.Copy(cm.sipRtpOut, sipRtpOut)

	// link rtcp path
	webrtcRtcpOut, err := NewGstReader(pipeline.WebrtcToSip.WebrtcRtcpAppSink)
	if err != nil {
		return nil, fmt.Errorf("failed to create WebRTC RTP writer: %w", err)
	}
	go cm.Copy(cm.webrtcRtcpOut, webrtcRtcpOut)

	return pipeline, nil
}

func (cm *CameraManager) WebrtcTrackInput(ti *TrackInput, sid string, ssrc uint32) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if cm.status != VideoStatusStarted {
		cm.log.Debugw("video manager not started", "status", cm.status)
		return
	}

	cm.log.Debugw("camera.webrtc.track_input", "sid", sid, "ssrc", ssrc)

	s, err := cm.pipeline.(*camera_pipeline.CameraPipeline).AddWebRTCSourceToSelector(ssrc)
	if err != nil {
		cm.log.Errorw("failed to add WebRTC source to selector", err)
		return
	}

	cm.ssrcs[sid] = ssrc

	webrtcRtpIn, err := NewGstWriter(s.RtpAppSrc)
	if err != nil {
		cm.log.Errorw("failed to create WebRTC RTP writer", err)
		return
	}
	go func() {
		cm.Copy(webrtcRtpIn, ti.RtpIn)
		if err := cm.pipeline.(*camera_pipeline.CameraPipeline).RemoveWebRTCSourceFromSelector(ssrc); err != nil {
			cm.log.Errorw("failed to remove WebRTC source from selector", err)
		}
	}()

	webrtcRtcpIn, err := NewGstWriter(s.RtcpAppSrc)
	if err != nil {
		cm.log.Errorw("failed to create WebRTC RTCP reader", err)
		return
	}
	go cm.Copy(webrtcRtcpIn, ti.RtcpIn)
}

func (cm *CameraManager) SwitchActiveWebrtcTrack(sid string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	ssrc, ok := cm.ssrcs[sid]
	if !ok {
		return fmt.Errorf("no SSRC found for sid %s", sid)
	}
	cm.log.Debugw("switching active WebRTC video track", "sid", sid, "ssrc", ssrc)

	if err := cm.pipeline.(*camera_pipeline.CameraPipeline).SwitchWebRTCSelectorSource(ssrc); err != nil {
		return fmt.Errorf("failed to switch WebRTC selector source: %w", err)
	}

	return nil
}
