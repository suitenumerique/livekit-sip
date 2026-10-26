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

package livekittracks

import (
	"errors"
	"testing"

	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/require"
)

func TestSinkTrackRenewTracks(t *testing.T) {
	newTrack := func(mime string) *lksdk.LocalTrack {
		track, err := lksdk.NewLocalTrack(webrtc.RTPCodecCapability{MimeType: mime})
		require.NoError(t, err)
		return track
	}
	s := &SinkTrack{}
	first := newTrack(webrtc.MimeTypeVP8)
	s.track.Store(first)
	s.stale.Store(true)

	_, _, err := s.renewTracks()
	require.Error(t, err, "no factory")
	require.Same(t, first, s.track.Load())

	s.factory = func() (*lksdk.LocalTrack, *lksdk.LocalTrack, error) {
		return newTrack(webrtc.MimeTypeVP8), newTrack(webrtc.MimeTypeH264), nil
	}
	oldID, newID, err := s.renewTracks()
	require.NoError(t, err)
	require.Equal(t, first.ID(), oldID)
	require.NotEqual(t, oldID, newID)
	require.Equal(t, newID, s.track.Load().ID())
	require.NotNil(t, s.backupTrack.Load())
	require.False(t, s.stale.Load())

	s.factory = func() (*lksdk.LocalTrack, *lksdk.LocalTrack, error) { return nil, nil, errors.New("boom") }
	_, _, err = s.renewTracks()
	require.Error(t, err)
	require.Equal(t, newID, s.track.Load().ID(), "a failed renewal keeps the current tracks")
}
