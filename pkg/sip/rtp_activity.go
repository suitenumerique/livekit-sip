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

package sip

import (
	"time"

	"github.com/livekit/sip/pkg/sip/pipeline"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/sipbin"
)

type rtpSourceKey struct {
	session int
	ssrc    uint32
}

type rtpSourceState struct {
	bytes uint64
	sr    uint64
}

// rtpActivity records when the device last sent RTP or RTCP sender reports.
// Counters are compared per remote source: a source that restarts from zero
// counts as activity as soon as it receives again, and a source that
// disappears does not hide the others.
type rtpActivity struct {
	sources map[rtpSourceKey]rtpSourceState
	last    time.Time
}

func (a *rtpActivity) observe(stats *pipeline.CallStats, now time.Time) {
	if stats == nil {
		return
	}
	next := make(map[rtpSourceKey]rtpSourceState)
	active := false
	for session, st := range []*sipbin.RTPSessionStats{stats.Microphone, stats.Camera, stats.ScreenShare} {
		if st == nil {
			continue
		}
		for _, src := range st.Sources {
			if src.Internal {
				continue
			}
			key := rtpSourceKey{session: session, ssrc: src.SSRC}
			cur := rtpSourceState{bytes: src.BytesReceived}
			if src.HaveSR {
				cur.sr = src.SRNTPTime
			}
			prev, known := a.sources[key]
			switch {
			case !known:
				active = active || cur.bytes > 0 || cur.sr != 0
			case cur.bytes != prev.bytes && cur.bytes > 0:
				active = true
			case cur.sr != prev.sr && cur.sr != 0:
				active = true
			}
			next[key] = cur
		}
	}
	a.sources = next
	if active {
		a.last = now
	}
}

func (a *rtpActivity) stalled(now time.Time, timeout time.Duration) bool {
	return a.last.IsZero() || now.Sub(a.last) > timeout
}
