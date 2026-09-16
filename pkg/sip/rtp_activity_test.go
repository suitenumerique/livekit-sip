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
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/livekit/sip/pkg/sip/pipeline"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/sipbin"
)

func remote(ssrc uint32, bytes uint64) sipbin.RTPSourceStats {
	return sipbin.RTPSourceStats{SSRC: ssrc, BytesReceived: bytes}
}

func callStats(mic, share []sipbin.RTPSourceStats) *pipeline.CallStats {
	return &pipeline.CallStats{
		Microphone:  &sipbin.RTPSessionStats{Sources: append([]sipbin.RTPSourceStats{{SSRC: 1, Internal: true, BytesReceived: 999}}, mic...)},
		ScreenShare: &sipbin.RTPSessionStats{Sources: share},
	}
}

func TestRtpActivity(t *testing.T) {
	start := time.Now()
	at := func(s int) time.Time { return start.Add(time.Duration(s) * time.Second) }
	var a rtpActivity

	require.True(t, a.stalled(at(0), RtpMediaTimeout), "no media seen yet")

	a.observe(callStats([]sipbin.RTPSourceStats{remote(10, 1000)}, []sipbin.RTPSourceStats{remote(20, 50_000_000)}), at(0))
	require.False(t, a.stalled(at(10), RtpMediaTimeout))

	// The screenshare source restarts from zero: the total drops while the
	// microphone keeps receiving. Seen in production on 16/09/2026.
	a.observe(callStats([]sipbin.RTPSourceStats{remote(10, 3000)}, []sipbin.RTPSourceStats{remote(20, 900_000)}), at(60))
	a.observe(callStats([]sipbin.RTPSourceStats{remote(10, 5000)}, []sipbin.RTPSourceStats{remote(20, 900_000)}), at(120))
	require.False(t, a.stalled(at(130), RtpMediaTimeout), "microphone still receiving")

	// A source disappearing is not activity.
	a.observe(callStats([]sipbin.RTPSourceStats{remote(10, 5000)}, nil), at(130))
	require.True(t, a.stalled(at(160), RtpMediaTimeout), "nothing received since 120 s")

	// A source that restarts from zero and receives again is activity.
	a.observe(callStats([]sipbin.RTPSourceStats{remote(10, 400)}, nil), at(170))
	require.False(t, a.stalled(at(180), RtpMediaTimeout))

	// Sender reports alone keep the call alive.
	sr := func(ntp uint64) *pipeline.CallStats {
		s := callStats([]sipbin.RTPSourceStats{remote(10, 400)}, nil)
		s.Microphone.Sources[1].HaveSR = true
		s.Microphone.Sources[1].SRNTPTime = ntp
		return s
	}
	a.observe(sr(1<<63|1), at(200))
	a.observe(sr(1<<63|2), at(240))
	require.False(t, a.stalled(at(260), RtpMediaTimeout))
	a.observe(sr(1<<63|2), at(300))
	require.True(t, a.stalled(at(300), RtpMediaTimeout), "same sender report, no bytes")

	a.observe(nil, at(310))
	require.True(t, a.stalled(at(310), RtpMediaTimeout))
}
