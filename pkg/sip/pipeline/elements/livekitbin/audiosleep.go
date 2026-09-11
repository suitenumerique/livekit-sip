// Copyright 2026 LiveKit, Inc.
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

package livekitbin

import (
	"fmt"
	"sort"
	"time"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

// audioTouch records the participants currently reported as speaking.
func (e *LivekitBin) audioTouch(p []lksdk.Participant) {
	if e.maxAudioParticipants == 0 {
		return
	}

	now := time.Now()
	e.audioMu.Lock()
	defer e.audioMu.Unlock()
	for _, part := range p {
		if _, ok := part.(*lksdk.RemoteParticipant); ok {
			e.audioLastActive[part.SID()] = now
		}
	}
}

func (e *LivekitBin) audioForget(sid string) {
	e.audioMu.Lock()
	defer e.audioMu.Unlock()
	delete(e.audioLastActive, sid)
}

// audioSleepLater runs audioSleep on the GLib main loop: the SDK invokes
// OnTrackPublished while holding the room lock that audioSleep reads.
func (e *LivekitBin) audioSleepLater() {
	if _, err := glib.IdleAdd(func() {
		e.livekitMu.Lock()
		defer e.livekitMu.Unlock()
		self := gst.ToGstBin(e.self.Get())
		if self == nil || self.Instance() == nil {
			return
		}
		e.audioSleep(self)
	}); err != nil {
		CAT.Log(gst.LevelError, fmt.Sprintf("Failed to add audio sleep to main loop\nerr=%v", err))
	}
}

// audioSleep enables the microphone tracks of the participants who spoke most
// recently, up to max-audio-participants (0 = all of them), and disables the
// other ones. Microphones stay subscribed either way: the SFU only reports
// the speakers we are subscribed to.
func (e *LivekitBin) audioSleep(self *gst.Bin) {
	limit := int(e.maxAudioParticipants)
	if !e.config.microphone || e.room == nil {
		return
	}

	type candidate struct {
		pub    *lksdk.RemoteTrackPublication
		sid    string
		active time.Time
	}

	// A participant who has not spoken yet ranks below every speaker: a
	// newcomer must not push the current speaker out of the window.
	e.audioMu.Lock()
	candidates := make([]candidate, 0, len(e.audioLastActive))
	for _, rp := range e.room.GetRemoteParticipants() {
		pub, ok := rp.GetTrackPublication(livekit.TrackSource_MICROPHONE).(*lksdk.RemoteTrackPublication)
		if !ok || pub == nil {
			continue
		}
		active := e.audioLastActive[rp.SID()]
		if pub.IsMuted() {
			active = time.Time{}
		}
		candidates = append(candidates, candidate{pub: pub, sid: rp.SID(), active: active})
	}
	e.audioMu.Unlock()

	sort.Slice(candidates, func(i, j int) bool {
		if !candidates[i].active.Equal(candidates[j].active) {
			return candidates[i].active.After(candidates[j].active)
		}
		return candidates[i].sid < candidates[j].sid
	})

	for i, c := range candidates {
		enabled := limit <= 0 || i < limit
		if enabled {
			if err := e.subscribeTrack(self, c.pub, "active-audio window"); err != nil {
				self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to subscribe to microphone track\ntrack=%s\nerr=%v", c.pub.SID(), err))
				continue
			}
		}
		if c.pub.IsEnabled() == enabled {
			continue
		}
		c.pub.SetEnabled(enabled)
		self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Audio track enabled state changed\ntrack=%s\nenabled=%t", c.pub.SID(), enabled))
	}
}
