// Copyright 2023 LiveKit, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// 	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package sip

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/frostbyte73/core"
	"github.com/pion/webrtc/v4"

	msdk "github.com/livekit/media-sdk"
	"github.com/livekit/media-sdk/dtmf"
	"github.com/livekit/media-sdk/rtp"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/logger/medialogutils"
	"github.com/livekit/protocol/sip"
	lksdk "github.com/livekit/server-sdk-go/v2"

	"github.com/livekit/media-sdk/mixer"

	"github.com/livekit/sip/pkg/config"
	"github.com/livekit/sip/pkg/media/opus"
)

type RoomStatsSnapshot struct {
	InputPackets uint64 `json:"input_packets"`
	InputBytes   uint64 `json:"input_bytes"`
	DTMFPackets  uint64 `json:"dtmf_packets"`

	PublishedFrames  uint64 `json:"published_frames"`
	PublishedSamples uint64 `json:"published_samples"`

	PublishTX float64 `json:"publish_tx"`

	MixerSamples uint64 `json:"mixer_samples"`
	MixerFrames  uint64 `json:"mixer_frames"`

	OutputSamples uint64 `json:"output_samples"`
	OutputFrames  uint64 `json:"output_frames"`

	Closed bool `json:"closed"`
}

type RoomStats struct {
	InputPackets atomic.Uint64
	InputBytes   atomic.Uint64
	DTMFPackets  atomic.Uint64

	PublishedFrames  atomic.Uint64
	PublishedSamples atomic.Uint64

	PublishTX atomic.Uint64

	MixerFrames  atomic.Uint64
	MixerSamples atomic.Uint64

	Mixer mixer.Stats

	OutputFrames  atomic.Uint64
	OutputSamples atomic.Uint64

	Closed atomic.Bool

	mu   sync.Mutex
	last struct {
		Time             time.Time
		PublishedSamples uint64
	}
}

func (s *RoomStats) Load() RoomStatsSnapshot {
	return RoomStatsSnapshot{
		InputPackets:     s.InputPackets.Load(),
		InputBytes:       s.InputBytes.Load(),
		DTMFPackets:      s.DTMFPackets.Load(),
		PublishedFrames:  s.PublishedFrames.Load(),
		PublishedSamples: s.PublishedSamples.Load(),
		PublishTX:        math.Float64frombits(s.PublishTX.Load()),
		MixerSamples:     s.MixerSamples.Load(),
		MixerFrames:      s.MixerFrames.Load(),
		OutputSamples:    s.OutputSamples.Load(),
		OutputFrames:     s.OutputFrames.Load(),
		Closed:           s.Closed.Load(),
	}
}

func (s *RoomStats) Update() {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := time.Now()
	dt := t.Sub(s.last.Time).Seconds()

	curPublishedSamples := s.PublishedSamples.Load()

	if dt > 0 {
		txSamples := curPublishedSamples - s.last.PublishedSamples

		txRate := float64(txSamples) / dt

		s.PublishTX.Store(math.Float64bits(txRate))
	}

	s.last.Time = t
	s.last.PublishedSamples = curPublishedSamples
}

type ParticipantInfo struct {
	ID       string
	RoomName string
	Identity string
	Name     string
}

// RoomInterface defines the interface for room operations
type RoomInterface interface {
	Connect(conf *config.Config, rconf RoomConfig) error
	Closed() <-chan struct{}
	Subscribed() <-chan struct{}
	Room() *lksdk.Room
	Subscribe()
	Output() msdk.Writer[msdk.PCM16Sample]
	SwapOutput(out msdk.PCM16Writer) msdk.PCM16Writer
	CloseOutput() error
	SetDTMFOutput(w dtmf.Writer)
	Close() error
	CloseWithReason(reason livekit.DisconnectReason) error
	Participant() ParticipantInfo
	NewParticipantTrack(sampleRate int) (msdk.WriteCloser[msdk.PCM16Sample], error)
	SendData(data lksdk.DataPacket, opts ...lksdk.DataPublishOption) error
	NewTrack() *mixer.Input
	IsReady() bool
}

type GetRoomFunc func(log logger.Logger, st *RoomStats) RoomInterface

func DefaultGetRoomFunc(log logger.Logger, st *RoomStats) RoomInterface {
	return NewRoom(log, st)
}

type Room struct {
	log        logger.Logger
	roomLog    logger.Logger // deferred logger
	room       *lksdk.Room
	mix        *mixer.Mixer
	out        *msdk.SwitchWriter
	outDtmf    atomic.Pointer[dtmf.Writer]
	p          ParticipantInfo
	ready      core.Fuse
	subscribe  atomic.Bool
	subscribed core.Fuse
	stopped    core.Fuse
	closed     core.Fuse
	stats      *RoomStats

	callbackHandler atomic.Pointer[RoomCallbacks]

	videoPublications map[string]*videoPublicationInfo
	activeVideoSID   string
	pendingVideoSID  string

	// Audio: subscribe all tracks for speaker detection, decode/mix only the last 6 speakers
	audioPublications map[string]*lksdk.RemoteTrackPublication
	audioTracks       map[string]*audioTrackInfo
	mixedAudioSIDs    []string                     // LRU list of SIDs currently decoded/mixed
	mixedCancel       map[string]context.CancelFunc // stop decode goroutine per mixed SID
	conf              *config.Config
}

type audioTrackInfo struct {
	track *webrtc.TrackRemote
	pub   *lksdk.RemoteTrackPublication
	rp    *lksdk.RemoteParticipant
}

type videoPublicationInfo struct {
	pub        *lksdk.RemoteTrackPublication
	rp         *lksdk.RemoteParticipant
	subscribed bool
}

// IsReady returns true if the room is connected and ready
func (r *Room) IsReady() bool {
	return r != nil && r.ready.IsBroken()
}

type ParticipantConfig struct {
	Identity   string
	Name       string
	Metadata   string
	Attributes map[string]string
}

type RoomConfig struct {
	WsUrl       string
	Token       string
	RoomName    string
	Participant ParticipantConfig
	RoomPreset  string
	RoomConfig  *livekit.RoomConfiguration
	JitterBuf   bool
}

type RoomCallbacks interface {
	WebrtcTrackSubscribed(track *webrtc.TrackRemote, pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) error
	WebrtcTrackUnsubscribed(track *webrtc.TrackRemote, pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) error
	ActiveParticipantChanged(p []lksdk.Participant) error
	LocalParticipantReady(p *lksdk.LocalParticipant) error
	Disconnect() error
}

func NewRoom(log logger.Logger, st *RoomStats) *Room {
	if st == nil {
		st = &RoomStats{}
	}
	r := &Room{
		log:               log,
		stats:             st,
		out:               msdk.NewSwitchWriter(RoomSampleRate),
		videoPublications: make(map[string]*videoPublicationInfo),
		audioPublications: make(map[string]*lksdk.RemoteTrackPublication),
		audioTracks:       make(map[string]*audioTrackInfo),
		mixedCancel:       make(map[string]context.CancelFunc),
	}
	out := newMediaWriterCount(r.out, &st.OutputFrames, &st.OutputSamples)

	var err error
	r.mix, err = mixer.NewMixer(out, rtp.DefFrameDur, &st.Mixer, 1, mixer.DefaultInputBufferFrames)
	if err != nil {
		panic(err)
	}

	roomLog, resolve := log.WithDeferredValues()
	r.roomLog = roomLog

	go func() {
		select {
		case <-r.ready.Watch():
			if r.room != nil {
				resolve.Resolve("room", r.room.Name(), "roomID", r.room.SID())
			} else {
				resolve.Resolve()
			}
			cb := r.callbackHandler.Load()
			if cb != nil {
				if err := (*cb).LocalParticipantReady(r.room.LocalParticipant); err != nil {
					r.log.Errorw("local participant ready callback error", err)
				}
			}
		case <-r.stopped.Watch():
			resolve.Resolve()
		case <-r.closed.Watch():
			resolve.Resolve()
		}
	}()

	return r
}

func (r *Room) SetCallbacks(cb RoomCallbacks) (oldCb RoomCallbacks) {
	oldPtr := r.callbackHandler.Swap(&cb)
	if oldPtr != nil {
		return *oldPtr
	}
	return nil
}

func (r *Room) Closed() <-chan struct{} {
	if r == nil {
		return nil
	}
	return r.stopped.Watch()
}

func (r *Room) Subscribed() <-chan struct{} {
	if r == nil {
		return nil
	}
	return r.subscribed.Watch()
}

func (r *Room) Room() *lksdk.Room {
	if r == nil {
		return nil
	}
	return r.room
}

func (r *Room) participantJoin(rp *lksdk.RemoteParticipant) {
	log := r.roomLog.WithValues("participant", rp.Identity(), "pID", rp.SID())
	log.Debugw("participant joined")
	switch rp.Kind() {
	case lksdk.ParticipantSIP:
		// Avoid a deadlock where two SIP participant join a room and won't publish their track.
		// Each waits for the other's track to subscribe before publishing its own track.
		// So we just assume SIP participants will eventually start speaking.
		r.subscribed.Break()
		log.Infow("unblocking subscription - second sip participant is in the room")
	}
}

func (r *Room) participantLeft(rp *lksdk.RemoteParticipant) {
	log := r.roomLog.WithValues("participant", rp.Identity(), "pID", rp.SID())
	log.Debugw("participant left")
	sid := rp.SID()
	delete(r.videoPublications, sid)
	if r.activeVideoSID == sid {
		r.activeVideoSID = ""
	}
	if r.pendingVideoSID == sid {
		r.pendingVideoSID = ""
	}
	r.stopMixing(sid)
	delete(r.audioPublications, sid)
	delete(r.audioTracks, sid)
}

func (r *Room) subscribeTo(pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
	log := r.roomLog.WithValues("participant", rp.Identity(), "pID", rp.SID(), "trackID", pub.SID(), "trackName", pub.Name())
	k := pub.Kind()
	if k != lksdk.TrackKindAudio && k != lksdk.TrackKindVideo {
		log.Debugw("skipping subscription to unsupported track", "kind", k)
		return
	}

	if k == lksdk.TrackKindAudio {
		// Subscribe all audio tracks for speaker detection
		r.audioPublications[rp.SID()] = pub
		if err := pub.SetSubscribed(true); err != nil {
			log.Errorw("cannot subscribe to audio track", err)
		}
		log.Infow("audio tracks: subscribing", "total", len(r.audioPublications), "mixed", len(r.mixedAudioSIDs), "maxMixed", maxMixedAudioTracks)
		r.subscribed.Break()
		return
	}

	// Store video publication for deferred subscription
	if pub.Source() == livekit.TrackSource_CAMERA {
		log.Debugw("storing video publication for deferred subscription")
		r.videoPublications[rp.SID()] = &videoPublicationInfo{pub: pub, rp: rp}
	} else if pub.Source() == livekit.TrackSource_SCREEN_SHARE {
		log.Debugw("subscribing to screenshare track")
		if err := pub.SetSubscribed(true); err != nil {
			log.Errorw("cannot subscribe to screenshare track", err)
		}
	}
}

const maxMixedAudioTracks = 6
const maxWarmVideoTracks = 2

// SwitchVideoSubscription switches to the given speaker's video track.
// Keeps up to maxWarmVideoTracks subscribed.
func (r *Room) SwitchVideoSubscription(newSID string) error {
	if r.activeVideoSID == newSID {
		r.log.Debugw("video switch skipped, same speaker", "sid", newSID)
		return nil
	}
	info, ok := r.videoPublications[newSID]
	if !ok {
		return fmt.Errorf("no video publication for SID %s", newSID)
	}
	if info.subscribed {
		r.log.Infow("switching to warm video track", "newSID", newSID, "oldSID", r.activeVideoSID)
		r.pendingVideoSID = ""
		r.activeVideoSID = newSID
		return nil
	}
	r.log.Infow("subscribing to new video track", "newSID", newSID, "oldSID", r.activeVideoSID)
	r.pendingVideoSID = newSID
	info.pub.SetSubscribed(true)
	info.subscribed = true
	return nil
}

// IsVideoTrackReady returns true if the given SID is the active video track.
func (r *Room) IsVideoTrackReady(sid string) bool {
	return r.activeVideoSID == sid
}

// onVideoTrackReady completes the make-before-break switch when a new track arrives.
func (r *Room) onVideoTrackReady(sid string) {
	if r.pendingVideoSID != sid {
		return
	}
	r.activeVideoSID = sid
	r.pendingVideoSID = ""
	r.evictOldVideoTracks()
}

// evictOldVideoTracks unsubscribes oldest non-active tracks beyond maxWarmVideoTracks limit.
func (r *Room) evictOldVideoTracks() {
	subscribed := 0
	for _, info := range r.videoPublications {
		if info.subscribed {
			subscribed++
		}
	}
	for subscribed > maxWarmVideoTracks {
		for sid, info := range r.videoPublications {
			if info.subscribed && sid != r.activeVideoSID && sid != r.pendingVideoSID {
				r.log.Infow("evicting warm video track", "sid", sid)
				info.pub.SetSubscribed(false)
				info.subscribed = false
				subscribed--
				break
			}
		}
	}
}

// UpdateActiveAudioSubscriptions promotes active speakers to the mixer
// and evicts the oldest from mixing. All tracks stay subscribed for speaker detection.
func (r *Room) UpdateActiveAudioSubscriptions(speakers []lksdk.Participant) {
	// Remove stale SIDs (participants who left)
	filtered := r.mixedAudioSIDs[:0]
	for _, sid := range r.mixedAudioSIDs {
		if _, ok := r.audioPublications[sid]; ok {
			filtered = append(filtered, sid)
		}
	}
	if len(r.mixedAudioSIDs) != len(filtered) {
		r.log.Infow("audio tracks: removed stale mixed SIDs", "before", len(r.mixedAudioSIDs), "after", len(filtered))
	}
	r.mixedAudioSIDs = filtered

	for _, speaker := range speakers {
		sid := speaker.SID()
		if _, ok := r.audioPublications[sid]; !ok {
			continue
		}
		// Move to end (most recent) if already mixed
		if r.isMixed(sid) {
			r.moveMixedToEnd(sid)
			continue
		}
		// New speaker: start mixing
		r.mixedAudioSIDs = append(r.mixedAudioSIDs, sid)
		r.startMixingTrack(sid, r.conf)
		r.log.Infow("audio tracks: promoted to mixer", "sid", sid, "mixed", len(r.mixedAudioSIDs))

		// Evict oldest from mixer if over limit
		for len(r.mixedAudioSIDs) > maxMixedAudioTracks {
			evictSID := r.mixedAudioSIDs[0]
			r.stopMixing(evictSID)
			r.log.Infow("audio tracks: evicted from mixer", "sid", evictSID, "mixed", len(r.mixedAudioSIDs))
		}
	}
}

// isMixed returns true if the SID is in the active mixer set
func (r *Room) isMixed(sid string) bool {
	for _, s := range r.mixedAudioSIDs {
		if s == sid {
			return true
		}
	}
	return false
}

// moveMixedToEnd moves a SID to the end of mixedAudioSIDs (most recent)
func (r *Room) moveMixedToEnd(sid string) {
	for i, s := range r.mixedAudioSIDs {
		if s == sid {
			r.mixedAudioSIDs = append(append(r.mixedAudioSIDs[:i], r.mixedAudioSIDs[i+1:]...), sid)
			return
		}
	}
}

// startMixingTrack starts the decode/mix goroutine for a subscribed audio track
func (r *Room) startMixingTrack(sid string, conf *config.Config) {
	info, ok := r.audioTracks[sid]
	if !ok {
		return // track not yet subscribed, will start when OnTrackSubscribed fires
	}
	if _, ok := r.mixedCancel[sid]; ok {
		return // already mixing
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.mixedCancel[sid] = cancel
	r.participantAudioTrackSubscribed(ctx, info.track, info.pub, info.rp, conf)
}

// stopMixing stops the decode/mix goroutine for a SID and removes from mixedAudioSIDs
func (r *Room) stopMixing(sid string) {
	if cancel, ok := r.mixedCancel[sid]; ok {
		cancel()
		delete(r.mixedCancel, sid)
	}
	for i, s := range r.mixedAudioSIDs {
		if s == sid {
			r.mixedAudioSIDs = append(r.mixedAudioSIDs[:i], r.mixedAudioSIDs[i+1:]...)
			return
		}
	}
}

// subscribeFirstVideoTrack subscribes to the first available camera track.
// Prefers the currently speaking participant, then mixed audio, then any.
func (r *Room) subscribeFirstVideoTrack() {
	if r.activeVideoSID != "" {
		return
	}
	// Prefer the participant who is currently speaking
	for _, rp := range r.room.GetRemoteParticipants() {
		if rp.IsSpeaking() {
			if info, ok := r.videoPublications[rp.SID()]; ok {
				r.log.Infow("subscribing to first video track (speaking)", "sid", rp.SID())
				info.pub.SetSubscribed(true)
				info.subscribed = true
				r.activeVideoSID = rp.SID()
				return
			}
		}
	}
	// Fallback: participant with active audio
	for _, sid := range r.mixedAudioSIDs {
		if info, ok := r.videoPublications[sid]; ok {
			r.log.Infow("subscribing to first video track (active audio)", "sid", sid)
			info.pub.SetSubscribed(true)
			info.subscribed = true
			r.activeVideoSID = sid
			return
		}
	}
	// Last resort: any video track
	for sid, info := range r.videoPublications {
		r.log.Infow("subscribing to first video track (fallback)", "sid", sid)
		info.pub.SetSubscribed(true)
		info.subscribed = true
		r.activeVideoSID = sid
		return
	}
}

// subscribeInitialAudioTracks marks the first 6 participants for mixing.
// Prefers currently speaking participants, fills remaining with others.
func (r *Room) subscribeInitialAudioTracks() {
	// Promote current speakers first
	var speakers []lksdk.Participant
	for _, rp := range r.room.GetRemoteParticipants() {
		if rp.IsSpeaking() {
			speakers = append(speakers, rp)
		}
	}
	if len(speakers) > 0 {
		r.UpdateActiveAudioSubscriptions(speakers)
	}

	// Fill remaining mix slots with non-speaking participants
	for sid := range r.audioPublications {
		if len(r.mixedAudioSIDs) >= maxMixedAudioTracks {
			break
		}
		if r.isMixed(sid) {
			continue
		}
		r.mixedAudioSIDs = append(r.mixedAudioSIDs, sid)
	}
	r.log.Infow("audio tracks: initial mix set", "mixed", len(r.mixedAudioSIDs), "total", len(r.audioPublications), "maxMixed", maxMixedAudioTracks)
}

func (r *Room) Connect(conf *config.Config, rconf RoomConfig) error {
	r.conf = conf
	if rconf.WsUrl == "" {
		rconf.WsUrl = conf.WsUrl
	}
	partConf := rconf.Participant
	r.p = ParticipantInfo{
		RoomName: rconf.RoomName,
		Identity: partConf.Identity,
		Name:     partConf.Name,
	}
	roomCallback := &lksdk.RoomCallback{
		OnParticipantConnected: func(rp *lksdk.RemoteParticipant) {
			log := r.roomLog.WithValues("participant", rp.Identity(), "pID", rp.SID())
			if !r.subscribe.Load() {
				log.Debugw("skipping participant join event - subscribed flag not set")
				return // will subscribe later
			}
			r.participantJoin(rp)
		},
		OnParticipantDisconnected: func(rp *lksdk.RemoteParticipant) {
			r.participantLeft(rp)
		},
		OnActiveSpeakersChanged: func(p []lksdk.Participant) {
			cb := r.callbackHandler.Load()
			if cb != nil {
				if err := (*cb).ActiveParticipantChanged(p); err != nil {
					r.log.Errorw("active participant changed callback error", err)
				}
			}
		},
		ParticipantCallback: lksdk.ParticipantCallback{
			OnTrackPublished: func(pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
				log := r.roomLog.WithValues("participant", rp.Identity(), "pID", rp.SID(), "trackID", pub.SID(), "trackName", pub.Name())
				if !r.subscribe.Load() {
					log.Debugw("skipping track publish event - subscribed flag not set")
					return // will subscribe later
				}
				r.subscribeTo(pub, rp)
			},
			OnTrackSubscribed: func(track *webrtc.TrackRemote, pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
				r.log.Debugw("track subscribed", "kind", pub.Kind(), "source", pub.Source(), "participant", rp.Identity(), "pID", rp.SID(), "trackID", pub.SID(), "trackName", pub.Name())
				if pub.Kind() == lksdk.TrackKindAudio && pub.Source() == livekit.TrackSource_MICROPHONE {
					// Store track reference for deferred mixing
					r.audioTracks[rp.SID()] = &audioTrackInfo{track: track, pub: pub, rp: rp}
					// Only decode/mix if this SID is in the mixed set
					if r.isMixed(rp.SID()) {
						r.startMixingTrack(rp.SID(), conf)
					}
					return
				}
				cb := r.callbackHandler.Load()
				if cb != nil {
					if err := (*cb).WebrtcTrackSubscribed(track, pub, rp); err != nil {
						r.log.Errorw("track subscribed callback error", err)
					}
				} else {
					r.log.Warnw("no track subscribed callback set", nil)
				}
				// Complete make-before-break video switch
				if pub.Kind() == lksdk.TrackKindVideo && pub.Source() == livekit.TrackSource_CAMERA {
					r.onVideoTrackReady(rp.SID())
				}
			},
			OnTrackUnsubscribed: func(track *webrtc.TrackRemote, pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
				r.log.Debugw("track unsubscribed", "kind", pub.Kind(), "source", pub.Source(), "participant", rp.Identity(), "pID", rp.SID(), "trackID", pub.SID(), "trackName", pub.Name())
				if pub.Kind() == lksdk.TrackKindAudio && pub.Source() == livekit.TrackSource_MICROPHONE {
					return // nothing to do
				}
				cb := r.callbackHandler.Load()
				if cb != nil {
					if err := (*cb).WebrtcTrackUnsubscribed(track, pub, rp); err != nil {
						r.log.Errorw("track unsubscribed callback error", err)
					}
				} else {
					r.log.Warnw("no track unsubscribed callback set", nil)
				}
			},
			OnDataPacket: func(data lksdk.DataPacket, params lksdk.DataReceiveParams) {
				switch data := data.(type) {
				case *livekit.SipDTMF:
					r.stats.InputPackets.Add(1)
					// TODO: Only generate audio DTMF if the message was a broadcast from another SIP participant.
					//       DTMF audio tone will be automatically mixed in any case.
					r.sendDTMF(data)
				}
			},
		},
		OnDisconnected: func() {
			cb := r.callbackHandler.Load()
			if cb != nil {
				if err := (*cb).Disconnect(); err != nil {
					r.log.Errorw("disconnect callback error", err)
				}
			}
			r.stopped.Break()
		},
	}

	if rconf.Token == "" {
		// TODO: Remove this code path, always sign tokens on LiveKit server.
		//       For now, match Cloud behavior and do not send extra attrs in the token.
		tokenAttrs := make(map[string]string, len(partConf.Attributes))
		for _, k := range []string{
			livekit.AttrSIPCallID,
			livekit.AttrSIPTrunkID,
			livekit.AttrSIPDispatchRuleID,
			livekit.AttrSIPTrunkNumber,
			livekit.AttrSIPPhoneNumber,
		} {
			if v, ok := partConf.Attributes[k]; ok {
				tokenAttrs[k] = v
			}
		}
		var err error
		rconf.Token, err = sip.BuildSIPToken(sip.SIPTokenParams{
			APIKey:                conf.ApiKey,
			APISecret:             conf.ApiSecret,
			RoomName:              rconf.RoomName,
			ParticipantIdentity:   partConf.Identity,
			ParticipantName:       partConf.Name,
			ParticipantMetadata:   partConf.Metadata,
			ParticipantAttributes: tokenAttrs,
			RoomPreset:            rconf.RoomPreset,
			RoomConfig:            rconf.RoomConfig,
		})
		if err != nil {
			return err
		}
	}
	room := lksdk.NewRoom(roomCallback)
	room.SetLogger(medialogutils.NewOverrideLogger(r.log))
	r.log.Infow("Attempting JoinWithToken", "wsUrl", rconf.WsUrl, "roomName", rconf.RoomName, "identity", partConf.Identity)
	err := room.JoinWithToken(rconf.WsUrl, rconf.Token,
		lksdk.WithAutoSubscribe(false),
		lksdk.WithExtraAttributes(partConf.Attributes),
	)
	if err != nil {
		r.log.Errorw("JoinWithToken failed", err, "wsUrl", rconf.WsUrl, "roomName", rconf.RoomName)
		return err
	}
	r.room = room
	r.p.ID = r.room.LocalParticipant.SID()
	r.p.Identity = r.room.LocalParticipant.Identity()
	r.log.Infow("JoinWithToken succeeded", "participantSID", r.p.ID, "identity", r.p.Identity)
	room.LocalParticipant.SetAttributes(partConf.Attributes)
	r.ready.Break()
	r.subscribe.Store(false) // already false, but keep for visibility

	// Not subscribing to any tracks just yet!
	return nil
}

func (r *Room) Subscribe() {
	if r.room == nil {
		return
	}
	r.subscribe.Store(true)
	list := r.room.GetRemoteParticipants()
	r.log.Debugw("subscribing to existing room participants", "participants", len(list))
	for _, rp := range list {
		r.participantJoin(rp)
		for _, pub := range rp.TrackPublications() {
			if remotePub, ok := pub.(*lksdk.RemoteTrackPublication); ok {
				r.subscribeTo(remotePub, rp)
			}
		}
	}
	// Subscribe first N audio tracks so there's audio before the first active speaker event
	r.subscribeInitialAudioTracks()
	r.subscribeFirstVideoTrack()
}

func (r *Room) Output() msdk.Writer[msdk.PCM16Sample] {
	return r.out.Get()
}

// SwapOutput sets room audio output and returns the old one.
// Caller is responsible for closing the old writer.
func (r *Room) SwapOutput(out msdk.PCM16Writer) msdk.PCM16Writer {
	if r == nil {
		return nil
	}
	if out == nil {
		return r.out.Swap(nil)
	}
	return r.out.Swap(msdk.ResampleWriter(out, r.mix.SampleRate()))
}

func (r *Room) CloseOutput() error {
	w := r.SwapOutput(nil)
	if w == nil {
		return nil
	}
	return w.Close()
}

func (r *Room) SetDTMFOutput(w dtmf.Writer) {
	if r == nil {
		return
	}
	if w == nil {
		r.outDtmf.Store(nil)
		return
	}
	r.outDtmf.Store(&w)
}

func (r *Room) sendDTMF(msg *livekit.SipDTMF) {
	outDTMF := r.outDtmf.Load()
	if outDTMF == nil {
		r.log.Infow("ignoring dtmf", "digit", msg.Digit)
		return
	}
	// TODO: Separate goroutine?
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.log.Infow("forwarding dtmf to sip", "digit", msg.Digit)
	_ = (*outDTMF).WriteDTMF(ctx, msg.Digit)
}

func (r *Room) Close() error {
	return r.CloseWithReason(livekit.DisconnectReason_UNKNOWN_REASON)
}

func (r *Room) CloseWithReason(reason livekit.DisconnectReason) error {
	if r == nil {
		return nil
	}
	var err error
	r.closed.Once(func() {
		defer r.stats.Closed.Store(true)

		r.subscribe.Store(false)
		err = r.CloseOutput()
		r.SetDTMFOutput(nil)
		if r.room != nil {
			r.room.DisconnectWithReason(reason)
			r.room = nil
		}
		if r.mix != nil {
			r.mix.Stop()
		}
	})
	return err
}

func (r *Room) Participant() ParticipantInfo {
	if r == nil {
		return ParticipantInfo{}
	}
	return r.p
}

func (r *Room) NewParticipantTrack(sampleRate int) (msdk.WriteCloser[msdk.PCM16Sample], error) {
	track, err := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus}, "audio", "pion")
	if err != nil {
		return nil, err
	}
	p := r.room.LocalParticipant
	if _, err = p.PublishTrack(track, &lksdk.TrackPublicationOptions{
		Name: p.Identity(),
	}); err != nil {
		return nil, err
	}
	ow := msdk.FromSampleWriter[opus.Sample](track, sampleRate, rtp.DefFrameDur)
	pw, err := opus.Encode(ow, channels, r.log)
	if err != nil {
		return nil, err
	}
	return newMediaWriterCount(pw, &r.stats.PublishedFrames, &r.stats.PublishedSamples), nil
}

func (r *Room) participantAudioTrackSubscribed(ctx context.Context, track *webrtc.TrackRemote, pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant, conf *config.Config) {
	log := r.roomLog.WithValues("participant", rp.Identity(), "pID", rp.SID(), "trackID", track.ID(), "trackName", pub.Name())
	if !r.ready.IsBroken() {
		log.Warnw("ignoring track, room not ready", nil)
		return
	}
	log.Infow("mixing track")

	go func() {
		mTrack := r.NewTrack()
		if mTrack == nil {
			return // closed
		}
		defer mTrack.Close()

		in := newRTPReaderCount(track, &r.stats.InputPackets, &r.stats.InputBytes)
		out := newMediaWriterCount(mTrack, &r.stats.MixerFrames, &r.stats.MixerSamples)

		odec, err := opus.Decode(out, channels, log)
		if err != nil {
			log.Errorw("cannot create opus decoder", err)
			return
		}
		defer odec.Close()

		var h rtp.HandlerCloser = rtp.NewNopCloser(rtp.NewMediaStreamIn[opus.Sample](odec))
		if conf.EnableJitterBuffer {
			h = rtp.HandleJitter(h)
		}

		// Decode loop — exits when context cancelled (eviction) or track ends
		done := make(chan error, 1)
		go func() { done <- rtp.HandleLoop(in, h) }()
		select {
		case err = <-done:
			if err != nil && !errors.Is(err, io.EOF) {
				log.Infow("room track rtp handler returned with failure", "error", err)
			}
		case <-ctx.Done():
			mTrack.Close()
			<-done
			log.Infow("mixing stopped (evicted)")
		}
	}()
}

func (r *Room) SendData(data lksdk.DataPacket, opts ...lksdk.DataPublishOption) error {
	if r == nil || !r.ready.IsBroken() || r.closed.IsBroken() {
		return nil
	}
	return r.room.LocalParticipant.PublishDataPacket(data, opts...)
}

func (r *Room) NewTrack() *mixer.Input {
	if r == nil {
		return nil
	}
	return r.mix.NewInput()
}
