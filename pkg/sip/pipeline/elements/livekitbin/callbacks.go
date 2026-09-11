package livekitbin

import (
	"fmt"
	"time"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4"
)

func (e *LivekitBin) callabcks() *lksdk.RoomCallback {
	return &lksdk.RoomCallback{
		OnDisconnected: func() {
			self := gst.ToGstBin(e.self.Get())
			if self == nil || self.Instance() == nil {
				return
			}
			self.Log(CAT, gst.LevelDebug, "Disconnected from LiveKit room, closing LivekitBin")
			if _, err := glib.IdleAdd(func() {
				e.livekitMu.Lock()
				defer e.livekitMu.Unlock()
				e.Close()
			}); err != nil {
				CAT.Log(gst.LevelError, fmt.Sprintf("Failed to add LivekitBin close to main loop\nerr=%v", err))
			}
		},
		OnParticipantConnected: func(rp *lksdk.RemoteParticipant) {
			if _, err := glib.IdleAdd(func() {
				e.livekitMu.Lock()
				defer e.livekitMu.Unlock()
				e.OnParticipantConnected(rp)
			}); err != nil {
				CAT.Log(gst.LevelError, fmt.Sprintf("Failed to add participant connection to main loop\nerr=%v", err))
			}
		},
		OnParticipantDisconnected: func(rp *lksdk.RemoteParticipant) {
			if _, err := glib.IdleAdd(func() {
				e.livekitMu.Lock()
				defer e.livekitMu.Unlock()
				e.OnParticipantDisconnected(rp)
			}); err != nil {
				CAT.Log(gst.LevelError, fmt.Sprintf("Failed to add participant disconnection to main loop\nerr=%v", err))
			}
		},
		ParticipantCallback: lksdk.ParticipantCallback{
			OnTrackSubscribed: func(track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
				self := gst.ToGstBin(e.self.Get())
				if self == nil || self.Instance() == nil {
					return
				}
				if err := e.Wait(RoomStateJoined); err != nil {
					self.Log(CAT, gst.LevelError, fmt.Sprintf("Error waiting for room to be joined\nerr=%v", err))
					self.Error(fmt.Sprintf("Error waiting for room to be joined: %v", err), err)
					return
				}
				// if err := e.Wait(RoomStatePlaying); err != nil {
				// 	self.Log(CAT, gst.LevelError, fmt.Sprintf("Error waiting for room to be playing: %v", err))
				// 	self.Error(fmt.Sprintf("Error waiting for room to be playing: %v", err), err)
				// 	return
				// }

				if _, err := glib.IdleAdd(func() {
					e.livekitMu.Lock()
					defer e.livekitMu.Unlock()
					e.SubscribeTrack(track, publication, rp)
					time.Sleep(5 * time.Millisecond)
				}); err != nil {
					CAT.Log(gst.LevelError, fmt.Sprintf("Failed to add track subscription to main loop\nerr=%v", err))
				}
			},
			OnTrackUnsubscribed: func(track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
				if _, err := glib.IdleAdd(func() {
					e.livekitMu.Lock()
					defer e.livekitMu.Unlock()
					e.UnsubscribeTrack(track, publication, rp)
				}); err != nil {
					CAT.Log(gst.LevelError, fmt.Sprintf("Failed to add track unsubscription to main loop\nerr=%v", err))
				}
			},
			OnTrackPublished: e.OnTrackPublished,
			OnTrackMuted: func(pub lksdk.TrackPublication, p lksdk.Participant) {
				if _, err := glib.IdleAdd(func() {
					e.livekitMu.Lock()
					defer e.livekitMu.Unlock()
					e.OnTrackMuted(pub, p)
				}); err != nil {
					CAT.Log(gst.LevelError, fmt.Sprintf("Failed to add track muted to main loop\nerr=%v", err))
				}
			},
			OnTrackUnmuted: func(pub lksdk.TrackPublication, p lksdk.Participant) {
				if _, err := glib.IdleAdd(func() {
					e.livekitMu.Lock()
					defer e.livekitMu.Unlock()
					e.OnTrackUnmuted(pub, p)
				}); err != nil {
					CAT.Log(gst.LevelError, fmt.Sprintf("Failed to add track unmuted to main loop\nerr=%v", err))
				}
			},
			OnLocalTrackPublished: func(pub *lksdk.LocalTrackPublication, _ *lksdk.LocalParticipant) {
				kind := pub.Source()
				switch kind {
				case livekit.TrackSource_CAMERA, livekit.TrackSource_MICROPHONE, livekit.TrackSource_SCREEN_SHARE, livekit.TrackSource_SCREEN_SHARE_AUDIO:
				default:
					return
				}

				if e.publications[kind] == nil || !e.publications[kind].initialized {
					return
				}

				if err := e.publications[kind].TrackSink.SetProperty("pub", glib.ArbitraryValue{Data: pub}); err != nil {
					self := gst.ToGstBin(e.self.Get())
					if self != nil && self.Instance() != nil {
						self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to set pub property on sink track for publication\npublication=%s\nerr=%v", pub.SID(), err))
						self.Error(fmt.Sprintf("Failed to set pub property on sink track for publication %s", pub.SID()), err)
					}
				}
			},
		},
		OnActiveSpeakersChanged: func(p []lksdk.Participant) {
			if _, err := glib.IdleAdd(func() {
				e.livekitMu.Lock()
				defer e.livekitMu.Unlock()
				e.OnActiveSpeakersChanged(p)
			}); err != nil {
				CAT.Log(gst.LevelError, fmt.Sprintf("Failed to add active speakers update to main loop\nerr=%v", err))
			}
		},
	}
}
