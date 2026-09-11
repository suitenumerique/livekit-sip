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
	"weak"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
)

// jitterbufferStatsPeriod is how often the receive statistics of every live
// jitterbuffer are logged at debug level.
const jitterbufferStatsPeriod = 10000 // ms

func jitterbufferKey(session, ssrc uint) uint64 {
	return uint64(session)<<32 | uint64(uint32(ssrc))
}

func (e *LivekitBin) rememberJitterbuffer(self *gst.Bin, jb *gst.Element, session, ssrc uint) {
	e.jbMu.Lock()
	defer e.jbMu.Unlock()
	if e.jitterbuffers == nil {
		e.jitterbuffers = make(map[uint64]*gst.Element)
	}
	jb.Ref()
	if old, ok := e.jitterbuffers[jitterbufferKey(session, ssrc)]; ok {
		old.Unref()
	}
	e.jitterbuffers[jitterbufferKey(session, ssrc)] = jb
	if e.jbStatsTimer {
		return
	}
	e.jbStatsTimer = true
	eweak := weak.Make(e)
	if _, err := glib.TimeoutAdd(jitterbufferStatsPeriod, func() bool {
		ptr := eweak.Value()
		if ptr == nil {
			return false
		}
		return ptr.logAllJitterbufferStats()
	}); err != nil {
		e.jbStatsTimer = false
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to schedule jitterbuffer stats\nerr=%v", err))
	}
}

func (e *LivekitBin) forgetJitterbuffer(session, ssrc uint) {
	e.jbMu.Lock()
	defer e.jbMu.Unlock()
	if jb, ok := e.jitterbuffers[jitterbufferKey(session, ssrc)]; ok {
		delete(e.jitterbuffers, jitterbufferKey(session, ssrc))
		jb.Unref()
	}
}

func jitterbufferStats(jb *gst.Element) string {
	v, err := jb.GetProperty("stats")
	if err != nil {
		return fmt.Sprintf("error=%v", err)
	}
	st, ok := v.(*gst.Structure)
	if !ok || st == nil {
		return "unavailable"
	}
	return st.String()
}

// logJitterbufferStats logs the receive statistics of one jitterbuffer, for
// instance right before rtpbin drops it.
func (e *LivekitBin) logJitterbufferStats(self *gst.Bin, session, ssrc uint, event string) {
	e.jbMu.Lock()
	jb, ok := e.jitterbuffers[jitterbufferKey(session, ssrc)]
	e.jbMu.Unlock()
	if !ok {
		return
	}
	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Jitterbuffer stats\nsession=%d\nssrc=%d\nevent=%s\n%s", session, ssrc, event, jitterbufferStats(jb)))
}

// logAllJitterbufferStats runs on the GLib main loop every
// jitterbufferStatsPeriod and stops once the bin is closed.
func (e *LivekitBin) logAllJitterbufferStats() bool {
	self := gst.ToGstBin(e.self.Get())
	if self == nil || self.Instance() == nil || e.Is(RoomStateClosed) {
		e.jbMu.Lock()
		e.jbStatsTimer = false
		e.jbMu.Unlock()
		return false
	}
	e.jbMu.Lock()
	keys := make([]uint64, 0, len(e.jitterbuffers))
	for k := range e.jitterbuffers {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	lines := make([]string, 0, len(keys))
	for _, k := range keys {
		lines = append(lines, fmt.Sprintf("session=%d ssrc=%d %s", k>>32, uint32(k), jitterbufferStats(e.jitterbuffers[k])))
	}
	e.jbMu.Unlock()
	for _, l := range lines {
		self.Log(CAT, gst.LevelDebug, "Jitterbuffer stats\nevent=periodic\n"+l)
	}
	return true
}
