package sip

import (
	"fmt"

	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4"
)

func (o *MediaOrchestrator) LocalParticipantReady(p *lksdk.LocalParticipant) error {
	if err := o.okStates(MediaStateReady); err != nil {
		return err
	}
	if err := o.tracks.ParticipantReady(p); err != nil {
		return fmt.Errorf("could not set participant ready: %w", err)
	}
	return nil
}

func (o *MediaOrchestrator) cameraTrackSubscribed(track *webrtc.TrackRemote, pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) error {
	if o.camera.Status() != VideoStatusStarted {
		return nil
	}
	ti := NewTrackInput(track, pub, rp)
	return o.camera.WebrtcTrackInput(ti, rp.SID(), uint32(track.SSRC()))
}

func (o *MediaOrchestrator) screenshareTrackSubscribed(track *webrtc.TrackRemote, pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) error {
	if o.screenshare == nil {
		o.log.Warnw("screenshare manager not initialized", nil)
		return nil
	}
	ti := NewTrackInput(track, pub, rp)
	return o.screenshare.WebrtcTrackInput(ti, rp.SID(), uint32(track.SSRC()))
}

func (o *MediaOrchestrator) webrtcTrackSubscribed(track *webrtc.TrackRemote, pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) error {
	log := o.log.WithValues("participant", rp.Identity(), "pID", rp.SID(), "trackID", pub.SID(), "trackName", pub.Name())
	switch pub.Kind() {
	case lksdk.TrackKindVideo:
		switch pub.Source() {
		case livekit.TrackSource_CAMERA:
			return o.tracks.CameraTracks.TrackSubscribed(track, pub, rp, o.cameraTrackSubscribed)
		case livekit.TrackSource_SCREEN_SHARE:
			log.Infow("screenshare track subscribed")
			return o.screenshareTrackSubscribed(track, pub, rp)
		}
	}
	log.Warnw("unsupported track kind for subscription", fmt.Errorf("kind=%s", pub.Kind()))
	return nil
}

func (o *MediaOrchestrator) WebrtcTrackSubscribed(track *webrtc.TrackRemote, pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) error {
	if err := o.dispatch(func() error {
		return o.webrtcTrackSubscribed(track, pub, rp)
	}); err != nil {
		return fmt.Errorf("could not handle webrtc track subscribed: %w", err)
	}
	return nil
}

func (o *MediaOrchestrator) cameraTrackUnsubscribed(_ *webrtc.TrackRemote, _ *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) error {
	if o.camera.Status() != VideoStatusStarted {
		return nil
	}
	return o.camera.RemoveWebrtcTrackInput(rp.SID())
}

func (o *MediaOrchestrator) screenshareTrackUnsubscribed(_ *webrtc.TrackRemote, _ *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) error {
	if o.screenshare == nil || !o.screenshare.IsActive() {
		return nil
	}
	o.log.Infow("screenshare track unsubscribed", "participant", rp.Identity())
	return o.screenshare.Stop()
}

func (o *MediaOrchestrator) webrtcTrackUnsubscribed(track *webrtc.TrackRemote, pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) error {
	log := o.log.WithValues("participant", rp.Identity(), "pID", rp.SID(), "trackID", pub.SID(), "trackName", pub.Name())
	switch pub.Kind() {
	case lksdk.TrackKindVideo:
		switch pub.Source() {
		case livekit.TrackSource_CAMERA:
			return o.tracks.CameraTracks.TrackUnsubscribed(track, pub, rp, o.cameraTrackUnsubscribed)
		case livekit.TrackSource_SCREEN_SHARE:
			log.Infow("screenshare track unsubscribed")
			return o.screenshareTrackUnsubscribed(track, pub, rp)
		}
	}
	log.Warnw("unsupported track kind for unsubscription", fmt.Errorf("kind=%s", pub.Kind()))
	return nil
}

func (o *MediaOrchestrator) WebrtcTrackUnsubscribed(track *webrtc.TrackRemote, pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) error {
	if err := o.dispatch(func() error {
		return o.webrtcTrackUnsubscribed(track, pub, rp)
	}); err != nil {
		return fmt.Errorf("could not handle webrtc track unsubscribed: %w", err)
	}
	return nil
}

func (o *MediaOrchestrator) activeParticipantChanged(p []lksdk.Participant) error {
	if o.camera.Status() != VideoStatusStarted {
		return nil
	}
	if len(p) == 0 {
		return nil
	}

	// Try each speaker until we find one with a video track
	for i, speaker := range p {
		sid := speaker.SID()
		if i == 0 {
			o.log.Infow("[SWITCH_DEBUG] Active speaker changed", "sid", sid, "totalSpeakers", len(p))
		}

		switched, err := o.camera.SwitchActiveWebrtcTrack(sid)
		if err != nil {
			o.log.Warnw("[SWITCH_DEBUG] Could not switch active webrtc track", err, "sid", sid)
			return nil
		}
		if switched {
			return nil
		}
	}
	return nil
}

func (o *MediaOrchestrator) ActiveParticipantChanged(p []lksdk.Participant) error {
	if err := o.dispatch(func() error {
		return o.activeParticipantChanged(p)
	}); err != nil {
		return fmt.Errorf("could not handle active participant changed: %w", err)
	}
	return nil
}

func (o *MediaOrchestrator) Disconnect() error {
	return o.Close()
}
