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
	"sync"
	"time"

	"github.com/livekit/protocol/livekit"

	"github.com/livekit/sip/pkg/sip/pipeline"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/sipbin"
)

const rtpStallMinSRPackets = 25

type rtpStallKey struct {
	session livekit.TrackSource
	ssrc    uint32
}

type rtpStallState struct {
	received uint64
	sr       uint32
	since    time.Time
}

type rtpStallEvent struct {
	Kind     string
	Session  livekit.TrackSource
	Source   sipbin.RTPSourceStats
	Rx       *sipbin.RxDiag
	SRDelta  uint32
	Resumed  bool
	Duration time.Duration
}

// rtpStall detects remote sources whose RTCP sender reports keep counting
// sent packets while no RTP packet reaches the RTP session.
type rtpStall struct {
	mu      sync.Mutex
	sources map[rtpStallKey]rtpStallState
}

func (s *rtpStall) observe(stats *pipeline.CallStats, now time.Time) []rtpStallEvent {
	if stats == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	next := make(map[rtpStallKey]rtpStallState)
	var events []rtpStallEvent
	for _, sess := range []struct {
		kind    string
		session livekit.TrackSource
		stats   *sipbin.RTPSessionStats
	}{
		{"microphone", livekit.TrackSource_MICROPHONE, stats.Microphone},
		{"camera", livekit.TrackSource_CAMERA, stats.Camera},
		{"screen-share", livekit.TrackSource_SCREEN_SHARE, stats.ScreenShare},
	} {
		if sess.stats == nil {
			continue
		}
		for _, src := range sess.stats.Sources {
			if src.Internal || !src.HaveSR {
				continue
			}
			key := rtpStallKey{session: sess.session, ssrc: src.SSRC}
			cur := rtpStallState{received: src.PacketsReceived, sr: src.SRPacketCount}
			prev, known := s.sources[key]
			if known {
				cur.since = prev.since
				srDelta := src.SRPacketCount - prev.sr
				ev := rtpStallEvent{Kind: sess.kind, Session: sess.session, Source: src, Rx: sess.stats.Rx, SRDelta: srDelta}
				switch {
				case src.PacketsReceived != prev.received:
					if !prev.since.IsZero() {
						ev.Resumed = true
						ev.Duration = now.Sub(prev.since)
						events = append(events, ev)
					}
					cur.since = time.Time{}
				case prev.since.IsZero() && srDelta >= rtpStallMinSRPackets && srDelta < 1<<31:
					cur.since = now
					events = append(events, ev)
				}
			}
			next[key] = cur
		}
	}
	s.sources = next
	return events
}
