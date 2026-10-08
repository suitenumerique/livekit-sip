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
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInternalSSRC(t *testing.T) {
	st := &RTPSessionStats{Sources: []RTPSourceStats{
		{SSRC: 1},
		{SSRC: 2, Internal: true},
		{SSRC: 3, Internal: true, IsSender: true},
	}}
	require.Equal(t, uint32(3), internalSSRC(st), "the sending internal source first")

	st.Sources = st.Sources[:2]
	require.Equal(t, uint32(2), internalSSRC(st), "the internal source that only reports")

	require.Zero(t, internalSSRC(&RTPSessionStats{Sources: []RTPSourceStats{{SSRC: 1}}}))
}

func TestRtcpSenderSSRC(t *testing.T) {
	var track SipTrack
	require.Equal(t, fallbackRTCPSenderSSRC, track.rtcpSenderSSRC())

	track.rtcpSSRC.Store(0x5eed)
	require.Equal(t, uint32(0x5eed), track.rtcpSenderSSRC())
}
