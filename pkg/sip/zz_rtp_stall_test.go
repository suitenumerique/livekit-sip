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

	"github.com/livekit/protocol/livekit"
	"github.com/stretchr/testify/require"

	"github.com/livekit/sip/pkg/sip/pipeline"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/sipbin"
)

func cameraSource(received uint64, sr uint32) *pipeline.CallStats {
	rx := &sipbin.RxDiag{InBySSRC: map[uint32]uint64{861957632: received + 2}}
	return &pipeline.CallStats{
		Camera: &sipbin.RTPSessionStats{
			Rx: rx,
			Sources: []sipbin.RTPSourceStats{
				{SSRC: 1, Internal: true, HaveSR: true, PacketsReceived: 5000, SRPacketCount: 5000},
				{SSRC: 861957632, HaveSR: true, PacketsReceived: received, SRPacketCount: sr, RTPFrom: "10.91.0.71:24348"},
			},
		},
	}
}

func TestRtpStall(t *testing.T) {
	start := time.Now()
	at := func(m int) time.Time { return start.Add(time.Duration(m) * time.Minute) }
	var s rtpStall

	require.Empty(t, s.observe(cameraSource(85476, 85702), at(0)))
	require.Empty(t, s.observe(cameraSource(89151, 89435), at(1)), "RTP and sender reports advance together")

	// 16/10/2026, Cisco DDI 34 Sete: RTP frozen at 90 257 while the sender
	// reports keep counting about 60 packets per second.
	require.Empty(t, s.observe(cameraSource(90257, 93207), at(2)), "RTP still advanced since the last check")
	ev := s.observe(cameraSource(90257, 96662), at(3))
	require.Len(t, ev, 1)
	require.False(t, ev[0].Resumed)
	require.Equal(t, "camera", ev[0].Kind)
	require.Equal(t, livekit.TrackSource_CAMERA, ev[0].Session)
	require.EqualValues(t, 861957632, ev[0].Source.SSRC)
	require.EqualValues(t, 96662-93207, ev[0].SRDelta)
	require.EqualValues(t, 90259, ev[0].Rx.InBySSRC[861957632])

	require.Empty(t, s.observe(cameraSource(90257, 100286), at(4)), "reported once per episode")

	ev = s.observe(cameraSource(90300, 104203), at(6))
	require.Len(t, ev, 1)
	require.True(t, ev[0].Resumed)
	require.Equal(t, 3*time.Minute, ev[0].Duration)
}

func TestRtpStallIgnoresQuietDevices(t *testing.T) {
	start := time.Now()
	at := func(m int) time.Time { return start.Add(time.Duration(m) * time.Minute) }
	var s rtpStall

	s.observe(cameraSource(1000, 1000), at(0))
	require.Empty(t, s.observe(cameraSource(1000, 1010), at(1)), "a few packets are not a stall")
	require.Empty(t, s.observe(cameraSource(1000, 1010), at(2)), "muted or on hold: no RTP and no new sender report")
	require.Empty(t, s.observe(cameraSource(1000, 3), at(3)), "sender report counter restarted")

	noSR := cameraSource(1000, 5000)
	noSR.Camera.Sources[1].HaveSR = false
	require.Empty(t, s.observe(noSR, at(4)))
	require.Empty(t, s.observe(cameraSource(1000, 9000), at(5)), "first sender report seen again starts a new baseline")

	require.Empty(t, s.observe(nil, at(6)))
}
