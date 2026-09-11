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
	"time"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
)

// subscriptionIdleGrace is how long a camera or microphone stays subscribed
// after leaving the mosaic / active-audio window, so a participant flapping in
// and out does not churn the SFU subscription and the decode chain.
const subscriptionIdleGrace = 10 * time.Second

type idleSubscription struct {
	since       time.Time
	timer       *time.Timer
	unsubscribe func() error
}

// markIdle schedules unsubscribe for sid once the grace period elapses. It is
// a no-op when sid is already scheduled.
func (e *LivekitBin) markIdle(sid string, unsubscribe func() error) {
	e.idleMu.Lock()
	defer e.idleMu.Unlock()
	if e.idle == nil {
		e.idle = make(map[string]*idleSubscription)
	}
	if _, ok := e.idle[sid]; ok {
		return
	}
	grace := e.idleGrace
	if grace <= 0 {
		grace = subscriptionIdleGrace
	}
	e.idle[sid] = &idleSubscription{
		since:       time.Now(),
		timer:       time.AfterFunc(grace, func() { e.idleExpired(sid) }),
		unsubscribe: unsubscribe,
	}
}

// cancelIdle keeps sid subscribed: the track is needed again.
func (e *LivekitBin) cancelIdle(sid string) bool {
	e.idleMu.Lock()
	defer e.idleMu.Unlock()
	s, ok := e.idle[sid]
	if !ok {
		return false
	}
	s.timer.Stop()
	delete(e.idle, sid)
	return true
}

func (e *LivekitBin) stopIdleTimers() {
	e.idleMu.Lock()
	defer e.idleMu.Unlock()
	for _, s := range e.idle {
		s.timer.Stop()
	}
	e.idle = nil
}

func (e *LivekitBin) takeIdle(sid string) (*idleSubscription, bool) {
	e.idleMu.Lock()
	defer e.idleMu.Unlock()
	s, ok := e.idle[sid]
	if ok {
		delete(e.idle, sid)
	}
	return s, ok
}

// idleExpired runs on the timer goroutine and hands the unsubscription to the
// GLib main loop, where every other room callback runs.
func (e *LivekitBin) idleExpired(sid string) {
	if _, err := glib.IdleAdd(func() {
		e.livekitMu.Lock()
		defer e.livekitMu.Unlock()
		self := gst.ToGstBin(e.self.Get())
		if self == nil || self.Instance() == nil || e.Is(RoomStateClosed) {
			return
		}
		s, ok := e.takeIdle(sid)
		if !ok {
			return
		}
		if err := s.unsubscribe(); err != nil {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to unsubscribe idle track\nsid=%s\nerr=%v", sid, err))
			return
		}
		self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Unsubscribed idle track\nsid=%s\nidle=%s", sid, time.Since(s.since).Round(time.Second)))
	}); err != nil {
		CAT.Log(gst.LevelError, fmt.Sprintf("Failed to add idle unsubscription to main loop\nsid=%s\nerr=%v", sid, err))
	}
}
