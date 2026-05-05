// Copyright 2025 LiveKit, Inc.
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

package sip

import (
	"testing"

	msdk "github.com/livekit/media-sdk"
	"github.com/livekit/protocol/logger"
)

func TestMarkForMixing_AddsNewSIDsUntilCap(t *testing.T) {
	r := NewRoom(logger.GetLogger(), nil)

	sids := []string{"A", "B", "C", "D", "E", "F", "G"}
	for _, sid := range sids {
		r.markForMixing(sid)
	}

	if got := len(r.mixedAudioSIDs); got != maxMixedAudioTracks {
		t.Fatalf("mixedAudioSIDs len = %d, want %d", got, maxMixedAudioTracks)
	}
	for i, sid := range sids[:maxMixedAudioTracks] {
		if r.mixedAudioSIDs[i] != sid {
			t.Errorf("mixedAudioSIDs[%d] = %q, want %q", i, r.mixedAudioSIDs[i], sid)
		}
	}
	if r.isMixed("G") {
		t.Errorf("seventh SID should not be in the mix set")
	}
}

func TestMarkForMixing_LateJoinerEntersMixSet(t *testing.T) {
	r := NewRoom(logger.GetLogger(), nil)

	r.markForMixing("P1")
	if !r.isMixed("P1") {
		t.Fatalf("P1 should be in the mix set after first markForMixing")
	}

	r.markForMixing("P2")
	if !r.isMixed("P2") {
		t.Fatalf("P2 (late joiner) must be in the mix set without speaker promotion")
	}
}

func TestMarkForMixing_Idempotent(t *testing.T) {
	r := NewRoom(logger.GetLogger(), nil)
	r.markForMixing("A")
	r.markForMixing("A")
	if got := len(r.mixedAudioSIDs); got != 1 {
		t.Fatalf("mixedAudioSIDs len = %d, want 1", got)
	}
}

func TestPCM16AudioLevel_SilenceIsQuietest(t *testing.T) {
	if got := pcm16AudioLevel(make(msdk.PCM16Sample, 320)); got != 127 {
		t.Fatalf("silence level = %d, want 127", got)
	}
}

func TestPCM16AudioLevel_FullScaleIsLoudest(t *testing.T) {
	pcm := make(msdk.PCM16Sample, 320)
	for i := range pcm {
		pcm[i] = 32767
	}
	if got := pcm16AudioLevel(pcm); got > 1 {
		t.Fatalf("full-scale level = %d, want ~0", got)
	}
}

func TestPCM16AudioLevel_MonotonicWithAmplitude(t *testing.T) {
	quiet := make(msdk.PCM16Sample, 320)
	loud := make(msdk.PCM16Sample, 320)
	for i := range quiet {
		quiet[i] = 100
		loud[i] = 10000
	}
	if pcm16AudioLevel(loud) >= pcm16AudioLevel(quiet) {
		t.Fatalf("loud=%d quiet=%d - level must decrease as amplitude rises (RFC 6464)", pcm16AudioLevel(loud), pcm16AudioLevel(quiet))
	}
}
