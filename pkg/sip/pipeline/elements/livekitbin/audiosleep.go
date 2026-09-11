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

// audioSleep enables the microphone tracks of the participants who spoke most
// recently, up to max-audio-participants, and disables the other ones.
func (e *LivekitBin) audioSleep(self *gst.Bin) {
	limit := int(e.maxAudioParticipants)
	if limit == 0 || !e.config.microphone || e.room == nil {
		return
	}

	type candidate struct {
		pub    *lksdk.RemoteTrackPublication
		active time.Time
	}

	e.audioMu.Lock()
	candidates := make([]candidate, 0, len(e.audioLastActive))
	for _, rp := range e.room.GetRemoteParticipants() {
		pub, ok := rp.GetTrackPublication(livekit.TrackSource_MICROPHONE).(*lksdk.RemoteTrackPublication)
		if !ok || pub == nil {
			continue
		}
		active, known := e.audioLastActive[rp.SID()]
		if !known {
			active = time.Now()
			e.audioLastActive[rp.SID()] = active
		}
		if pub.IsMuted() {
			active = time.Time{}
		}
		candidates = append(candidates, candidate{pub: pub, active: active})
	}
	e.audioMu.Unlock()

	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].active.After(candidates[j].active)
	})

	for i, c := range candidates {
		enabled := i < limit
		if c.pub.IsEnabled() == enabled {
			continue
		}
		c.pub.SetEnabled(enabled)
		self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Audio track enabled state changed\ntrack=%s\nenabled=%t", c.pub.SID(), enabled))
	}
}
