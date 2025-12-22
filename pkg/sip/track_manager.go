package sip

import (
	"errors"
	"fmt"

	"github.com/frostbyte73/core"
	"github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4"
)

type remoteTrackInfo struct {
	track *webrtc.TrackRemote
	pub   *lksdk.RemoteTrackPublication
	rp    *lksdk.RemoteParticipant
}

func newTrackKindManager(tm *TrackManager) trackKindManager {
	return trackKindManager{
		tm:     tm,
		tracks: make(map[string]remoteTrackInfo),
	}
}

type trackKindManager struct {
	tm     *TrackManager
	tracks map[string]remoteTrackInfo
}

func (t *trackKindManager) TrackSubscribed(
	track *webrtc.TrackRemote,
	pub *lksdk.RemoteTrackPublication,
	rp *lksdk.RemoteParticipant,
	then func(track *webrtc.TrackRemote, pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) error) error {
	if !t.tm.ready.IsBroken() {
		t.tm.log.Warnw("track manager not ready yet, waiting", nil)
		<-t.tm.ready.Watch()
		t.tm.log.Infow("track manager is now ready", nil)
	}

	sid := rp.SID()
	if _, ok := t.tracks[sid]; ok {
		t.tm.log.Warnw("track already exists for participant", nil, "SID", sid)
	}

	t.tracks[sid] = remoteTrackInfo{
		track: track,
		pub:   pub,
		rp:    rp,
	}

	if then != nil {
		return then(track, pub, rp)
	}

	return nil
}

func (t *trackKindManager) TrackUnsubscribed(
	track *webrtc.TrackRemote,
	pub *lksdk.RemoteTrackPublication,
	rp *lksdk.RemoteParticipant,
	then func(track *webrtc.TrackRemote, pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) error) error {
	if !t.tm.ready.IsBroken() {
		t.tm.log.Warnw("track manager not ready yet, waiting", nil)
		<-t.tm.ready.Watch()
		t.tm.log.Infow("track manager is now ready", nil)
	}

	sid := rp.SID()
	info, ok := t.tracks[sid]
	if !ok {
		return fmt.Errorf("no track for participant %s", sid)
	}

	delete(t.tracks, sid)

	if then != nil {
		return then(info.track, info.pub, info.rp)
	}

	return nil
}

func (t *trackKindManager) Tracks() map[string]remoteTrackInfo {
	if !t.tm.ready.IsBroken() {
		t.tm.log.Warnw("track manager not ready yet, waiting", nil)
		<-t.tm.ready.Watch()
		t.tm.log.Infow("track manager is now ready", nil)
	}

	copied := make(map[string]remoteTrackInfo, len(t.tracks))
	for k, v := range t.tracks {
		copied[k] = v
	}
	return copied
}

func NewTrackManager(log logger.Logger) *TrackManager {
	tm := &TrackManager{
		log: log.WithComponent("TrackManager"),
	}
	tm.CameraTracks = newTrackKindManager(tm)
	return tm
}

type TrackManager struct {
	ready core.Fuse
	log   logger.Logger
	p     *lksdk.LocalParticipant

	camera       *TrackOutput
	CameraTracks trackKindManager
}

func (tm *TrackManager) Participant() *lksdk.LocalParticipant {
	if !tm.ready.IsBroken() {
		tm.log.Warnw("track manager not ready yet, waiting", nil)
		<-tm.ready.Watch()
		tm.log.Infow("track manager is now ready")
	}
	return tm.p
}

func (tm *TrackManager) ParticipantReady(p *lksdk.LocalParticipant) error {
	if tm.ready.IsBroken() {
		return fmt.Errorf("track manager is already ready")
	}
	tm.p = p
	tm.ready.Break()
	return nil
}

func (tm *TrackManager) Camera() (*TrackOutput, error) {
	if tm.camera != nil {
		return tm.camera, nil
	}

	to := &TrackOutput{}

	p := tm.Participant()

	track, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{
		MimeType: webrtc.MimeTypeVP8,
	}, "video", "pion")
	if err != nil {
		return nil, fmt.Errorf("could not create room camera track: %w", err)
	}
	pt, err := p.PublishTrack(track, &lksdk.TrackPublicationOptions{
		Name: p.Identity(),
	})
	if err != nil {
		return nil, err
	}
	tm.log.Infow("published camera track", "SID", pt.SID())
	trackRtcp := &RtcpWriter{
		pc: tm.p.GetSubscriberPeerConnection(),
	}
	to.RtpOut = &CallbackWriteCloser{
		Writer: track,
		Callback: func() error {
			tm.log.Infow("unpublishing video track", "SID", pt.SID())
			pt.CloseTrack()
			tm.camera = nil
			return nil
		},
	}
	to.RtcpOut = &NopWriteCloser{Writer: trackRtcp}

	tm.camera = to

	return to, nil
}

func (tm *TrackManager) Close() error {
	var errs []error

	if tm.camera != nil {
		if err := tm.camera.RtpOut.Close(); err != nil {
			errs = append(errs, fmt.Errorf("could not close camera RTP output: %w", err))
		}
		if err := tm.camera.RtcpOut.Close(); err != nil {
			errs = append(errs, fmt.Errorf("could not close camera RTCP output: %w", err))
		}
		tm.camera = nil
	}

	return errors.Join(errs...)
}
