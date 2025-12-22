package sip

import (
	"context"
	"fmt"
	"net/netip"

	sdpv2 "github.com/livekit/media-sdk/sdp/v2"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/sip/pkg/sip/pipeline/camera_pipeline"
)

type CameraManager struct {
	*VideoManager
	ssrcs map[string]uint32
	tm    *TrackManager
}

func NewCameraManager(log logger.Logger, ctx context.Context, room *Room, opts *MediaOptions, tm *TrackManager) (*CameraManager, error) {
	cm := &CameraManager{
		ssrcs: make(map[string]uint32),
		tm:    tm,
	}

	vm, err := NewVideoManager(log, ctx, opts, cm)
	if err != nil {
		return nil, err
	}
	cm.VideoManager = vm

	return cm, nil
}

func (cm *CameraManager) publishCameraTrack() error {
	if cm.status != VideoStatusStarted {
		cm.log.Errorw("video manager not started, cannot publish camera track", nil, "status", cm.status)
		return nil
	}

	to, err := cm.tm.Camera()
	if err != nil {
		return fmt.Errorf("could not get camera track output: %w", err)
	}

	if err := cm.webrtcTrackOutput(to); err != nil {
		return fmt.Errorf("could not set webrtc track output: %w", err)
	}

	return nil
}

func (cm *CameraManager) Reconcile(remote netip.Addr, media *sdpv2.SDPMedia) (ReconcileStatus, error) {
	rs, err := cm.VideoManager.Reconcile(remote, media)
	if err != nil {
		return rs, err
	}

	return rs, nil
}

func (cm *CameraManager) Start() error {
	if err := cm.VideoManager.Start(); err != nil {
		return err
	}

	// if rs == ReconcileStatusUpdated {
	// 	if err := cm.RecoverTracks(); err != nil {
	// 		cm.log.Errorw("failed to recover camera tracks", err)
	// 		return rs, err
	// 	}
	// }

	if err := cm.publishCameraTrack(); err != nil {
		cm.log.Errorw("failed to publish camera track", err)
		return err
	}
	return nil
}

func (cm *CameraManager) CreateVideoPipeline(opt *MediaOptions) (SipPipeline, error) {
	pipeline, err := camera_pipeline.New(cm.ctx, cm.log)
	if err != nil {
		return nil, fmt.Errorf("failed to create SIP WebRTC pipeline: %w", err)
	}
	// pipeline.Monitor()

	return pipeline, nil
}

func (cm *CameraManager) WebrtcTrackInput(ti *TrackInput, sid string, ssrc uint32) error {
	if cm.status != VideoStatusStarted {
		cm.log.Errorw("video manager not started, cannot add WebRTC track input", nil, "status", cm.status)
		return fmt.Errorf("video manager not started")
	}

	cm.log.Infow("WebRTC video track subscribed - connecting WebRTC→SIP pipeline",
		"hasRtpIn", ti.RtpIn != nil,
		"hasRtcpIn", ti.RtcpIn != nil)

	p := cm.pipeline.(*camera_pipeline.CameraPipeline)

	_, err := p.AddWebrtcTrack(ssrc, ti.RtpIn, ti.RtcpIn)
	if err != nil {
		cm.log.Errorw("failed to add WebRTC source to selector", err)
		return fmt.Errorf("failed to add WebRTC source to selector: %w", err)
	}

	cm.ssrcs[sid] = ssrc
	return nil
}

func (cm *CameraManager) RemoveWebrtcTrackInput(sid string) error {
	ssrc, ok := cm.ssrcs[sid]
	if !ok {
		return fmt.Errorf("no SSRC found for sid %s", sid)
	}
	cm.log.Debugw("removing WebRTC video track input", "sid", sid, "ssrc", ssrc)

	p := cm.pipeline.(*camera_pipeline.CameraPipeline)

	if err := p.RemoveWebrtcTrack(ssrc); err != nil {
		return fmt.Errorf("failed to remove WebRTC source from selector: %w", err)
	}

	delete(cm.ssrcs, sid)
	return nil
}

func (cm *CameraManager) webrtcTrackOutput(to *TrackOutput) error {
	cm.log.Infow("WebRTC video track published - connecting SIP→WebRTC pipeline",
		"hasRtpOut", to.RtpOut != nil,
		"hasRtcpOut", to.RtcpOut != nil)

	p := cm.pipeline.(*camera_pipeline.CameraPipeline)

	if err := p.WebrtcOutput(to.RtpOut, to.RtcpOut); err != nil {
		cm.log.Errorw("failed to write SIP RTP to WebRTC", err)
		return fmt.Errorf("failed to write SIP RTP to WebRTC: %w", err)
	}

	// rtpMo, err := NewMediaOutput(cm.ctx, to.RtpOut, pipeline.SipToWebrtc.WebrtcRtpAppSink)
	// if err != nil {
	// 	cm.log.Errorw("failed to create WebRTC RTP media output", err)
	// 	return fmt.Errorf("failed to create WebRTC RTP media output: %w", err)
	// }
	// if err := cm.io.AddOutputs(rtpMo); err != nil {
	// 	cm.log.Errorw("failed to add WebRTC RTP media output", err)
	// 	return fmt.Errorf("failed to add WebRTC RTP media output: %w", err)
	// }

	// rtcpMo, err := NewMediaOutput(cm.ctx, to.RtcpOut, pipeline.WebrtcToSip.WebrtcRtcpAppSink)
	// if err != nil {
	// 	cm.log.Errorw("failed to create WebRTC RTCP media output", err)
	// 	return fmt.Errorf("failed to create WebRTC RTCP media output: %w", err)
	// }
	// if err := cm.io.AddOutputs(rtcpMo); err != nil {
	// 	cm.log.Errorw("failed to add WebRTC RTCP media output", err)
	// 	return fmt.Errorf("failed to add WebRTC RTCP media output: %w", err)
	// }

	return nil
}

func (cm *CameraManager) SwitchActiveWebrtcTrack(sid string) error {
	ssrc, ok := cm.ssrcs[sid]
	if !ok {
		return fmt.Errorf("no SSRC found for sid %s", sid)
	}
	cm.log.Debugw("switching active WebRTC video track", "sid", sid, "ssrc", ssrc)

	p := cm.pipeline.(*camera_pipeline.CameraPipeline)

	if err := p.DirtySwitchWebrtcInput(ssrc); err != nil {
		return fmt.Errorf("failed to switch WebRTC selector source: %w", err)
	}

	return nil
}

func (cm *CameraManager) RecoverTracks() error {
	return nil
	// cm.mu.Lock()
	// defer cm.mu.Unlock()

	// if cm.status != VideoStatusStarted {
	// 	return nil
	// }

	// time.Sleep(1 * time.Second) // wait a bit to ensure pipeline is ready

	// cm.log.Infow("recovering camera tracks after video reconnection")
	// tracks := cm.tm.CameraTracks.Tracks()
	// for sid, track := range tracks {
	// 	// if track.pub.IsSubscribed() {
	// 	// 	continue
	// 	// }
	// 	cm.log.Infow("recovering camera track", "sid", sid, "isSubscribed", track.pub.IsSubscribed())
	// 	track.pub.SetSubscribed(true)
	// }

	// return nil
}
