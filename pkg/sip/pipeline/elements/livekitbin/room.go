package livekitbin

import (
	"fmt"
	"runtime"
	"sort"
	"time"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	protoCodecs "github.com/livekit/protocol/codecs"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/apperror"
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
		self.Log(CAT, gst.LevelError, fmt.Sprintf("WebSocket URL and token must be set before connecting to a LiveKit room\nws_url=%s\ntoken=%s", e.wsURL, e.token))
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
		lksdk.WithDisableTURN(),
	); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error connecting to LiveKit room\nerr=%v", err))
		self.ErrorMessage(apperror.AppErrorDomain, apperror.AppFatalError, fmt.Sprintf("Error connecting to LiveKit room: %v", err), "")
		return
	}
	self.Log(CAT, gst.LevelInfo, "Successfully joined LiveKit room, waiting for connection to be established...")
	if err := roomWaitConnected(e.room); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error waiting for LiveKit room connection\nerr=%v", err))
		self.ErrorMessage(apperror.AppErrorDomain, apperror.AppFatalError, "Error waiting for LiveKit room connection", err.Error())
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
	if self == nil || self.Instance() == nil || e.Is(RoomStateClosed) {
		return
	}

	e.Set(RoomStateClosed)
	e.stopIdleTimers()

	if e.room == nil {
		return
	}

	self.Log(CAT, gst.LevelInfo, "Closing LivekitBin and disconnecting from LiveKit room")

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

	self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Active speakers changed\nspeakers=%v", lo.Map(p, func(part lksdk.Participant, i int) string { return part.SID() })))

	if !e.Is(RoomStateJoined) {
		self.Log(CAT, gst.LevelWarning, "Received active speakers changed callback while not joined to a room")
		return
	}

	p = lo.Filter(p, func(part lksdk.Participant, i int) bool {
		_, ok := part.(*lksdk.RemoteParticipant)
		return ok
	})

	e.audioTouch(p)

	e.updateActiveSpeakers(self, p)
}

func (e *LivekitBin) OnTrackPublished(publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
	self := gst.ToGstBin(e.self.Get())
	if self == nil || self.Instance() == nil {
		return
	}

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Track published\nparticipant=%s\ntrack=%s\nsource=%s", rp.Identity(), publication.Name(), publication.Source().String()))

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
		self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Not subscribing to track publication\nparticipant=%s\nsource=%s", rp.Identity(), publication.Source().String()))
		return
	}

	if !enabled {
		self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Not subscribing to track publication due to configuration\nparticipant=%s\nsource=%s", rp.Identity(), publication.Source().String()))
		return
	}

	switch publication.Source() {
	case livekit.TrackSource_CAMERA, livekit.TrackSource_MICROPHONE:
		// Subscribed on demand by cameraSleep/audioSleep: participants outside
		// the mosaic and the active-audio window cost no decode chain at all.
		self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Deferring track subscription to the layout scheduler\nparticipant=%s\nsource=%s", rp.Identity(), publication.Source().String()))
		e.refreshSubscriptionsLater()
		return
	}

	if err := publication.SetSubscribed(true); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to subscribe to track publication\nsource=%s\nparticipant=%s\nerr=%v", publication.Source(), rp.Identity(), err))
		self.Error(fmt.Sprintf("Failed to subscribe to %s track publication for participant %s", publication.Source(), rp.Identity()), err)
		return
	}
	e.requestHighQuality(self, publication, rp.Identity())
	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Subscribed to track publication\nsource=%s\nparticipant=%s", publication.Source(), rp.Identity()))
}

// refreshSubscriptionsLater re-evaluates the mosaic and active-audio window on
// the GLib main loop: the SDK invokes OnTrackPublished while holding the room
// lock that the schedulers read.
func (e *LivekitBin) refreshSubscriptionsLater() {
	if _, err := glib.IdleAdd(func() {
		e.livekitMu.Lock()
		defer e.livekitMu.Unlock()
		self := gst.ToGstBin(e.self.Get())
		if self == nil || self.Instance() == nil || e.room == nil || e.Is(RoomStateClosed) {
			return
		}
		e.mu.Lock()
		defer e.mu.Unlock()
		e.updateActiveSpeakers(self, e.getCurrentActiveSpeakers())
	}); err != nil {
		CAT.Log(gst.LevelError, fmt.Sprintf("Failed to add subscription refresh to main loop\nerr=%v", err))
	}
}

// requestHighQuality asks the SFU for the top simulcast/SVC layer of a
// screenshare subscription.
func (e *LivekitBin) requestHighQuality(self *gst.Bin, publication *lksdk.RemoteTrackPublication, identity string) {
	if publication.Source() != livekit.TrackSource_SCREEN_SHARE {
		return
	}
	if err := publication.SetVideoQuality(livekit.VideoQuality_HIGH); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to request high quality for screenshare\nparticipant=%s\nerr=%v", identity, err))
	}
}

func (e *LivekitBin) OnParticipantConnected(rp *lksdk.RemoteParticipant) {
	self := gst.ToGstBin(e.self.Get())
	if self == nil || self.Instance() == nil {
		return
	}

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Participant connected\nparticipant=%s", rp.SID()))
	e.announce(self, rp)

	e.mu.Lock()
	defer e.mu.Unlock()

	e.updateActiveSpeakers(self, e.getCurrentActiveSpeakers())
}

func (e *LivekitBin) OnParticipantDisconnected(rp *lksdk.RemoteParticipant) {
	self := gst.ToGstBin(e.self.Get())
	if self == nil || self.Instance() == nil {
		return
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	e.audioForget(rp.SID())
	e.forget(rp.SID())
	for _, pub := range rp.TrackPublications() {
		e.cancelIdle(pub.SID())
	}
	e.updateActiveSpeakers(self, lo.Filter(e.getCurrentActiveSpeakers(), func(p lksdk.Participant, _ int) bool {
		return p.SID() != rp.SID()
	}))

	self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Participant disconnected\nparticipant=%s", rp.SID()))
	if _, err := self.Emit("participant-left", livekittracks.NewParticipantInfo(rp).Structure()); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error emitting participant-left signal\nerr=%v", err))
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
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Track publication is not a remote track publication\nparticipant=%s\npublication=%v", participant.Identity(), publication))
		return
	}

	if pub == nil || pub.TrackRemote() == nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Remote track is nil for publication\ntrack=%s\nparticipant=%s", pub.SID(), participant.Identity()))
		return
	}
	ssrc := pub.TrackRemote().SSRC()

	if pub.Source() == livekit.TrackSource_MICROPHONE {
		e.audioSleep(self)
	}

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
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error emitting clear-ssrc signal\ntrack=%s\nparticipant=%s\nerr=%v", pub.SID(), participant.SID(), err))
			self.Error(fmt.Sprintf("Error emitting clear-ssrc signal for track %s of participant %s", pub.SID(), participant.SID()), err)
			return
		}

		self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Muted track\nsource=%s\ntrack=%s\nssrc=%d\nparticipant=%s", pub.Source(), pub.SID(), ssrc, participant.SID()))
	}()
}

func (e *LivekitBin) OnTrackUnmuted(publication lksdk.TrackPublication, participant lksdk.Participant) {
	self := gst.ToGstBin(e.self.Get())
	if self == nil || self.Instance() == nil {
		return
	}

	pub, ok := publication.(*lksdk.RemoteTrackPublication)
	if !ok {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Track publication is not a remote track publication\nparticipant=%s\npublication=%v", participant.Identity(), publication))
		return
	}

	if pub == nil || pub.TrackRemote() == nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Remote track is nil for publication\ntrack=%s\nparticipant=%s", pub.SID(), participant.Identity()))
		return
	}
	ssrc := pub.TrackRemote().SSRC()

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Unmuted track\nsource=%s\ntrack=%s\nssrc=%d\nparticipant=%s", pub.Source(), pub.SID(), ssrc, participant.SID()))

	if pub.Source() == livekit.TrackSource_MICROPHONE {
		e.audioTouch([]lksdk.Participant{participant})
		e.audioSleep(self)
	}
}

func (e *LivekitBin) getCurrentActiveSpeakers() []lksdk.Participant {
	return lo.Filter(lo.Map(e.room.GetRemoteParticipants(), func(participant *lksdk.RemoteParticipant, i int) lksdk.Participant {
		return participant
	}), func(participant lksdk.Participant, i int) bool {
		return lo.Contains(e.activeSpeakers, participant.SID())
	})

}

func (e *LivekitBin) updateActiveSpeakers(self *gst.Bin, p []lksdk.Participant) {
	present := make(map[string]*lksdk.RemoteParticipant)
	for _, rp := range e.room.GetRemoteParticipants() {
		present[rp.SID()] = rp
	}
	speakers := make([]string, 0, len(p))
	for _, part := range p {
		rp, ok := part.(*lksdk.RemoteParticipant)
		if !ok {
			continue
		}
		present[rp.SID()] = rp
		speakers = append(speakers, rp.SID())
	}

	// Candidates for the free slots in arrival order: a stable order keeps the
	// mosaic from reshuffling on every event (map iteration is random).
	others := lo.Keys(present)
	e.partMu.Lock()
	now := time.Now()
	for _, sid := range others {
		if _, ok := e.seenAt[sid]; !ok {
			e.seenAt[sid] = now
		}
	}
	sort.SliceStable(others, func(i, j int) bool {
		ti, tj := e.seenAt[others[i]], e.seenAt[others[j]]
		if ti.Equal(tj) {
			return others[i] < others[j]
		}
		return ti.Before(tj)
	})
	e.partMu.Unlock()

	maxActive := int(e.maxActiveParticipants)
	if maxActive == 0 {
		maxActive = MAX_ACTIVE_PARTICIPANTS
	}
	e.activeSpeakers = nextLayout(e.activeSpeakers, speakers, others, maxActive)

	members := make([]lksdk.Participant, 0, len(e.activeSpeakers))
	for _, sid := range e.activeSpeakers {
		rp := present[sid]
		// The compositor must know every member before the layout names it.
		e.announce(self, rp)
		members = append(members, rp)
	}

	self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Active speakers updated\nspeakers=%v", e.activeSpeakers))

	// Subscribe and wake the tiles first so the compositor finds their pads
	// when it applies the new layout.
	e.cameraSleep(self, members)
	e.audioSleep(self)

	structure := livekittracks.NewActiveSpeakerChangeInfo(members).Structure()
	if _, err := self.Emit("active-speakers-changed", structure.Transfer()); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error emitting active-speakers-changed signal\nerr=%v", err))
		self.Error("Error emitting active-speakers-changed signal", err)
		return
	}
}

// announce introduces rp to the compositor once (participant-join) and records
// its arrival for the mosaic fill order.
func (e *LivekitBin) announce(self *gst.Bin, rp *lksdk.RemoteParticipant) {
	if rp == nil {
		return
	}
	sid := rp.SID()
	e.partMu.Lock()
	_, done := e.announced[sid]
	e.announced[sid] = struct{}{}
	if _, ok := e.seenAt[sid]; !ok {
		e.seenAt[sid] = time.Now()
	}
	e.partMu.Unlock()
	if done {
		return
	}
	if _, err := self.Emit("participant-join", livekittracks.NewParticipantInfo(rp).Structure()); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error emitting participant-join signal\nerr=%v", err))
		self.Error("Error emitting participant-join signal", err)
	}
}

// forget drops the bookkeeping of a participant that left.
func (e *LivekitBin) forget(sid string) {
	e.partMu.Lock()
	delete(e.announced, sid)
	delete(e.seenAt, sid)
	e.partMu.Unlock()
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
				self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to subscribe to track\ntrack=%s\nparticipant=%s\nerr=%v", config.kind, participant.Identity(), err))
			} else {
				e.requestHighQuality(self, pub, participant.Identity())
				changed = true
			}
		}
	}
	if changed {
		self.Log(CAT, gst.LevelInfo, "Track subscription states updated")
		e.updateActiveSpeakers(self, e.getCurrentActiveSpeakers())
	}
}
