package livekitbin

import (
	"fmt"
	"runtime"
	"time"

	"github.com/go-gst/go-gst/gst"
	protoCodecs "github.com/livekit/protocol/codecs"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/livekitbin/livekittracks"
	"github.com/pion/webrtc/v4"
	"github.com/samber/lo"
)

func supportedCodecs(in []livekit.Codec) []livekit.Codec {
	seen := make(map[webrtc.PayloadType]bool, len(in))
	out := make([]livekit.Codec, 0, len(in))
	for _, c := range in {
		p := protoCodecs.ToWebrtcCodecParameters(&c) // protoCodecs "github.com/livekit/protocol/codecs"
		if p.MimeType == "" || seen[p.PayloadType] {
			continue // unmapped (e.g. G722) or PT already taken
		}
		seen[p.PayloadType] = true
		out = append(out, c)
	}
	return out
}

func (e *LivekitBin) OnConnectSignal(instance *gst.Element) {
	self := gst.ToGstBin(instance)

	e.livekitMu.Lock()
	defer e.livekitMu.Unlock()

	if e.Set(RoomStateJoining)&RoomStateJoining != 0 {
		self.Log(CAT, gst.LevelWarning, "Already joining a LiveKit room")
		return
	}

	defer e.Unset(RoomStateJoining)

	if e.Is(RoomStateJoined) {
		self.Log(CAT, gst.LevelWarning, "Already connected to a LiveKit room")
		return
	}

	if e.wsURL == "" || e.token == "" {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("WebSocket URL and token must be set before connecting to a LiveKit room (ws-url: %s, token: %s)", e.wsURL, e.token))
		self.Error("WebSocket URL and token must be set before connecting to a LiveKit room", fmt.Errorf("invalid config: ws-url: %s, token: %s", e.wsURL, e.token))
		return
	}

	codecs := lo.Map(append(AudioCodecMimeTypes, VideoCodecMimeTypes...), func(mimeType string, _ int) livekit.Codec {
		return livekit.Codec{
			Mime: mimeType,
		}
	})
	codecs = supportedCodecs(codecs)

	self.Log(CAT, gst.LevelInfo, "Connecting to LiveKit room...")
	if err := e.room.JoinWithToken(e.wsURL, e.token,
		lksdk.WithAutoSubscribe(false),
		lksdk.WithExtraAttributes(e.defaultParticipantAttributes),
		lksdk.WithCodecs(codecs),
	); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error connecting to LiveKit room: %v", err))
		self.Error("Error connecting to LiveKit room", err)
		return
	}
	self.Log(CAT, gst.LevelInfo, "Successfully joined LiveKit room, waiting for connection to be established...")
	if err := roomWaitConnected(e.room); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error waiting for LiveKit room connection: %v", err))
		self.Error("Error waiting for LiveKit room connection", err)
		return
	}

	e.Set(RoomStateJoined)
	e.Unset(RoomStateJoining)
	self.Log(CAT, gst.LevelInfo, "Connected to LiveKit room")

	for _, rp := range e.room.GetRemoteParticipants() {
		e.OnParticipantConnected(rp)
	}

	if _, err := self.Emit("connected"); err != nil {
		self.Log(CAT, gst.LevelError, "Error emitting connected signal")
		self.Error("Error emitting connected signal", err)
	}
}

func roomWaitConnected(room *lksdk.Room) error {
	for {
		state := room.ConnectionState()
		if state == lksdk.ConnectionStateConnected {
			break
		}
		if state == lksdk.ConnectionStateDisconnected {
			return fmt.Errorf("disconnected while joining room")
		}
		runtime.Gosched()
	}
	for _, pc := range []*webrtc.PeerConnection{
		room.LocalParticipant.GetPublisherPeerConnection(),
		room.LocalParticipant.GetSubscriberPeerConnection(),
	} {
		for lo.Contains([]webrtc.PeerConnectionState{
			webrtc.PeerConnectionStateNew,
			webrtc.PeerConnectionStateConnecting,
		}, pc.ConnectionState()) {
			runtime.Gosched()
		}
		if state := pc.ConnectionState(); state != webrtc.PeerConnectionStateConnected {
			return fmt.Errorf("peer connection not connected after joining room: %s", state.String())
		}
	}
	return nil
}

func (e *LivekitBin) Close() {
	self := gst.ToGstBin(e.self.Get())
	if self == nil || self.Instance() == nil {
		return
	}

	self.Log(CAT, gst.LevelInfo, "Closing LivekitBin and disconnecting from LiveKit room")

	e.Set(RoomStateClosed)

	if e.room == nil {
		return
	}

	if e.room.ConnectionState() != lksdk.ConnectionStateDisconnected {
		e.room.Disconnect()
	}

	if _, err := self.Emit("closed"); err != nil {
		self.Log(CAT, gst.LevelError, "Error emitting closed signal")
		self.Error("Error emitting closed signal", err)
	}
	self.Log(CAT, gst.LevelInfo, "Disconnected from LiveKit room")

}

func (e *LivekitBin) OnActiveSpeakersChanged(p []lksdk.Participant) {
	self := gst.ToGstBin(e.self.Get())
	if self == nil || self.Instance() == nil {
		return
	}

	if e.Is(RoomStateClosed) {
		self.Log(CAT, gst.LevelWarning, "Received active speakers changed callback after room was closed")
		return
	}

	self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Active speakers changed: %v", lo.Map(p, func(part lksdk.Participant, i int) string { return part.SID() })))

	if !e.Is(RoomStateJoined) {
		self.Log(CAT, gst.LevelWarning, "Received active speakers changed callback while not joined to a room")
		return
	}

	p = lo.Filter(p, func(part lksdk.Participant, i int) bool {
		_, ok := part.(*lksdk.RemoteParticipant)
		return ok
	})

	maxParticipants := e.maxActiveParticipants
	if maxParticipants == 0 {
		maxParticipants = MAX_ACTIVE_PARTICIPANTS
	}

	if len(p) >= int(maxParticipants) {
		p = p[:maxParticipants]
	} else {
		for _, sid := range e.activeSpeakers {
			if len(p) >= int(maxParticipants) {
				break
			}
			if lo.ContainsBy(p, func(part lksdk.Participant) bool { return part.SID() == sid }) {
				continue
			}
			part := e.room.GetParticipantBySID(sid)
			if part == nil {
				self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Could not find participant with SID %s", sid))
				continue
			}
			p = append(p, part)
		}
	}

	e.updateActiveSpeakers(self, p)
}

func (e *LivekitBin) OnTrackPublished(publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
	self := gst.ToGstBin(e.self.Get())
	if self == nil || self.Instance() == nil {
		return
	}

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Track published by participant %s: %s (source: %s)", rp.Identity(), publication.Name(), publication.Source().String()))

	var enabled bool
	switch publication.Source() {
	case livekit.TrackSource_CAMERA:
		enabled = e.config.camera
	case livekit.TrackSource_MICROPHONE:
		enabled = e.config.microphone
	case livekit.TrackSource_SCREEN_SHARE:
		enabled = e.config.screenshare
	case livekit.TrackSource_SCREEN_SHARE_AUDIO:
		enabled = e.config.screenshareAudio
	default:
		self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Not subscribing to track publication for participant %s of kind %s", rp.Identity(), publication.Source().String()))
		return
	}

	if !enabled {
		self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Not subscribing to track publication for participant %s of kind %s due to configuration", rp.Identity(), publication.Source().String()))
		return
	}

	if err := publication.SetSubscribed(true); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to subscribe to %s track publication for participant %s: %v", publication.Source(), rp.Identity(), err))
		self.Error(fmt.Sprintf("Failed to subscribe to %s track publication for participant %s", publication.Source(), rp.Identity()), err)
		return
	}
	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Subscribed to %s track publication for participant %s", publication.Source(), rp.Identity()))
}

func (e *LivekitBin) OnParticipantConnected(rp *lksdk.RemoteParticipant) {
	self := gst.ToGstBin(e.self.Get())
	if self == nil || self.Instance() == nil {
		return
	}

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Participant connected: %s", rp.SID()))
	if _, err := self.Emit("participant-join", livekittracks.NewParticipantInfo(rp).Structure()); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error emitting participant-join signal: %v", err))
		self.Error("Error emitting participant-join signal", err)
		return
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	e.updateActiveSpeakers(self, append(e.getCurrentActiveSpeakers(), rp))
}

func (e *LivekitBin) OnParticipantDisconnected(rp *lksdk.RemoteParticipant) {
	self := gst.ToGstBin(e.self.Get())
	if self == nil || self.Instance() == nil {
		return
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	e.updateActiveSpeakers(self, lo.Filter(e.getCurrentActiveSpeakers(), func(p lksdk.Participant, _ int) bool {
		return p.SID() != rp.SID()
	}))

	self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Participant disconnected: %s", rp.SID()))
	if _, err := self.Emit("participant-left", livekittracks.NewParticipantInfo(rp).Structure()); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error emitting participant-left signal: %v", err))
		self.Error("Error emitting participant-left signal", err)
		return
	}
}

func (e *LivekitBin) OnTrackMuted(publication lksdk.TrackPublication, participant lksdk.Participant) {
	self := gst.ToGstBin(e.self.Get())
	if self == nil || self.Instance() == nil {
		return
	}

	pub, ok := publication.(*lksdk.RemoteTrackPublication)
	if !ok {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Track publication is not a remote track publication for participant %s: %v", participant.Identity(), publication))
		return
	}

	if pub == nil || pub.TrackRemote() == nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Remote track is nil for publication %s of participant %s", pub.SID(), participant.Identity()))
		return
	}
	ssrc := pub.TrackRemote().SSRC()

	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		time.Sleep(100 * time.Millisecond)

		e.mu.Lock()
		defer e.mu.Unlock()

		if !publication.IsMuted() {
			return
		}

		if _, err := e.RtpBin.Emit("clear-ssrc", uint32(pub.Source()), uint(ssrc)); err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error emitting clear-ssrc signal for track %s of participant %s: %v", pub.SID(), participant.SID(), err))
			self.Error(fmt.Sprintf("Error emitting clear-ssrc signal for track %s of participant %s", pub.SID(), participant.SID()), err)
			return
		}

		self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Muted track %s(%s:%d) of participant %s", pub.Source(), pub.SID(), ssrc, participant.SID()))
	}()
}

func (e *LivekitBin) OnTrackUnmuted(publication lksdk.TrackPublication, participant lksdk.Participant) {
	self := gst.ToGstBin(e.self.Get())
	if self == nil || self.Instance() == nil {
		return
	}

	pub, ok := publication.(*lksdk.RemoteTrackPublication)
	if !ok {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Track publication is not a remote track publication for participant %s: %v", participant.Identity(), publication))
		return
	}

	if pub == nil || pub.TrackRemote() == nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Remote track is nil for publication %s of participant %s", pub.SID(), participant.Identity()))
		return
	}
	ssrc := pub.TrackRemote().SSRC()

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Unmuted track %s(%s:%d) of participant %s", pub.Source(), pub.SID(), ssrc, participant.SID()))
}

func (e *LivekitBin) getCurrentActiveSpeakers() []lksdk.Participant {
	return lo.Filter(lo.Map(e.room.GetRemoteParticipants(), func(participant *lksdk.RemoteParticipant, i int) lksdk.Participant {
		return participant
	}), func(participant lksdk.Participant, i int) bool {
		return lo.Contains(e.activeSpeakers, participant.SID())
	})

}

func (e *LivekitBin) updateActiveSpeakers(self *gst.Bin, p []lksdk.Participant) {
	p = lo.Filter(p, func(part lksdk.Participant, i int) bool {
		_, ok := part.(*lksdk.RemoteParticipant)
		return ok
	})
	activeSpeakers := lo.Map(p, func(part lksdk.Participant, i int) string { return part.SID() })
	activeSpeakers = lo.Uniq(activeSpeakers)

	rp := lo.Map(p, func(part lksdk.Participant, i int) *lksdk.RemoteParticipant {
		return part.(*lksdk.RemoteParticipant)
	})
	rp = append(rp, e.room.GetRemoteParticipants()...)
	rp = lo.UniqBy(rp, func(part *lksdk.RemoteParticipant) string {
		return part.SID()
	})
	activeSpeakers = lo.Filter(activeSpeakers, func(sid string, i int) bool {
		if !lo.ContainsBy(rp, func(part *lksdk.RemoteParticipant) bool {
			return part.SID() == sid
		}) {
			return false
		}
		return true
	})

	maxActive := int(e.maxActiveParticipants)
	if maxActive == 0 {
		maxActive = MAX_ACTIVE_PARTICIPANTS
	}

	if len(activeSpeakers) > maxActive {
		activeSpeakers = activeSpeakers[:maxActive]
	}

	if len(activeSpeakers) < maxActive {
		for _, part := range rp {
			if len(activeSpeakers) >= maxActive {
				break
			}
			if lo.Contains(activeSpeakers, part.SID()) {
				continue
			}
			activeSpeakers = append(activeSpeakers, part.SID())
		}
	}

	e.activeSpeakers = activeSpeakers

	p = lo.Filter(lo.Map(rp, func(part *lksdk.RemoteParticipant, _ int) lksdk.Participant {
		return part
	}), func(part lksdk.Participant, i int) bool {
		return lo.Contains(e.activeSpeakers, part.SID())
	})

	self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Active speakers updated: %v", activeSpeakers))

	structure := livekittracks.NewActiveSpeakerChangeInfo(p).Structure()
	if _, err := self.Emit("active-speakers-changed", structure.Transfer()); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error emitting active-speakers-changed signal: %v", err))
		self.Error("Error emitting active-speakers-changed signal", err)
		return
	}

	e.cameraSleep(self, p)
}

func (e *LivekitBin) updateSubscriptions(self *gst.Bin) {
	trackConfig := []struct {
		kind    livekit.TrackSource
		enabled bool
	}{
		{livekit.TrackSource_CAMERA, e.camera},
		{livekit.TrackSource_MICROPHONE, e.microphone},
		{livekit.TrackSource_SCREEN_SHARE, e.screenshare},
		{livekit.TrackSource_SCREEN_SHARE_AUDIO, e.screenshareAudio},
	}

	changed := false
	for _, participant := range e.room.GetRemoteParticipants() {
		for _, config := range trackConfig {
			if !config.enabled {
				continue
			}
			pub, ok := participant.GetTrackPublication(config.kind).(*lksdk.RemoteTrackPublication)
			if !ok || pub == nil {
				continue
			}
			if pub.IsSubscribed() {
				continue
			}
			if err := pub.SetSubscribed(true); err != nil {
				self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to subscribe to track %s of participant %s: %v", config.kind, participant.Identity(), err))
			} else {
				changed = true
			}
		}
	}
	if changed {
		self.Log(CAT, gst.LevelInfo, "Track subscription states updated")
		e.updateActiveSpeakers(self, e.getCurrentActiveSpeakers())
	}
}
