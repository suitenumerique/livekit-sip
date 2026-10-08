// Copyright 2026 LiveKit, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package sipbin

import (
	"fmt"
	"sync"
	"time"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/livekit"
)

const (
	trackToggleRetryInterval = 250 * time.Millisecond
	trackToggleRetryTimeout  = 3 * time.Second
)

type trackToggles struct {
	mu  sync.Mutex
	gen [NbTracks]uint64
}

func (t *trackToggles) begin(kind livekit.TrackSource) uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.gen[kind]++
	return t.gen[kind]
}

func (t *trackToggles) push(kind livekit.TrackSource, gen uint64, f func() error) (bool, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.gen[kind] != gen {
		return true, nil
	}
	return false, f()
}

func (e *SipBin) setTrackToggle(self *gst.Bin, kind livekit.TrackSource, on bool) {
	gen := e.toggles.begin(kind)
	push := func(self *gst.Bin) (bool, error) {
		return e.toggles.push(kind, gen, func() error { return e.trackToggleEvent(self, kind, on) })
	}
	_, err := push(self)
	if err == nil {
		return
	}
	self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to toggle track, retrying\nsource=%s\non=%t\nerr=%v", kind, on, err))

	wself := glib.WeakRefInit(self)
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		ticker := time.NewTicker(trackToggleRetryInterval)
		defer ticker.Stop()
		deadline := time.Now().Add(trackToggleRetryTimeout)
		for range ticker.C {
			self := gst.ToGstBin(wself.Get())
			if self == nil || self.Instance() == nil {
				return
			}
			stale, err := push(self)
			switch {
			case stale:
				return
			case err == nil:
				self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Toggled track after retry\nsource=%s\non=%t", kind, on))
				return
			case time.Now().After(deadline):
				self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Gave up toggling track\nsource=%s\non=%t\nerr=%v", kind, on, err))
				return
			}
		}
	}()
}
