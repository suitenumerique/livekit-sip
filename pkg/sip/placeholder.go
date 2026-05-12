// Copyright 2023 LiveKit, Inc.
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
	"context"
	"encoding/json"
	"time"

	msdk "github.com/livekit/media-sdk"
	"github.com/livekit/media-sdk/mixer"
	"github.com/livekit/media-sdk/rtp"

	"github.com/livekit/sip/res"
)

const (
	placeholderSilenceDuration = 30 * time.Second
	placeholderPollInterval    = 1 * time.Second
)

// roomEncryptionState mirrors the encryption-related fields the backend pushes
// into LiveKit room metadata when a Room is created or mutated. Missing fields
// default to "not encrypted".
type roomEncryptionState struct {
	IsEncrypted      bool `json:"is_encrypted"`
	EncryptionPaused bool `json:"encryption_paused"`
}

// IsLive reports whether E2EE is active right now: declared encrypted AND not
// currently paused by an admin. Plain-SIP cannot bridge a live-encrypted room.
func (s roomEncryptionState) IsLive() bool {
	return s.IsEncrypted && !s.EncryptionPaused
}

func parseRoomEncryptionState(metadata string) roomEncryptionState {
	var s roomEncryptionState
	if metadata == "" {
		return s
	}
	_ = json.Unmarshal([]byte(metadata), &s)
	return s
}

// playEncryptionPlaceholderLoop plays the encryption-blocked prompt to the
// SIP caller on a loop ("prompt → 30s silence → prompt …") and returns
// when ctx is cancelled. The caller (encryption watcher in inbound.go) is
// responsible for cancelling when the admin pauses encryption.
//
// Writes through a mixer input — the LK room mixer is already wired into
// the SIP audio writer via SwapOutput, so two concurrent producers would
// interleave samples and scramble the prompt.
func playEncryptionPlaceholderLoop(
	ctx context.Context,
	track *mixer.Input,
	prompt []msdk.PCM16Sample,
) {
	if track == nil || len(prompt) == 0 {
		return
	}

	frames := prompt
	if track.SampleRate() != res.SampleRate {
		frames = make([]msdk.PCM16Sample, len(prompt))
		for i := range prompt {
			frames[i] = msdk.Resample(nil, track.SampleRate(), prompt[i], res.SampleRate)
		}
	}

	perFrameSamples := track.SampleRate() / msdk.DefFramesPerSec
	silenceFrame := make(msdk.PCM16Sample, perFrameSamples)
	silenceFrameCount := int(placeholderSilenceDuration / rtp.DefFrameDur)
	silenceFrames := make([]msdk.PCM16Sample, silenceFrameCount)
	for i := range silenceFrames {
		silenceFrames[i] = silenceFrame
	}

	for {
		if err := msdk.PlayAudio[msdk.PCM16Sample](ctx, track, rtp.DefFrameDur, frames); err != nil {
			return
		}
		if err := msdk.PlayAudio[msdk.PCM16Sample](ctx, track, rtp.DefFrameDur, silenceFrames); err != nil {
			return
		}
	}
}
