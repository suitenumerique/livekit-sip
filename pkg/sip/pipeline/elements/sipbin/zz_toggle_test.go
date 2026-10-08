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
	"errors"
	"testing"

	"github.com/livekit/protocol/livekit"
	"github.com/stretchr/testify/require"
)

func TestTrackToggles(t *testing.T) {
	var toggles trackToggles
	share := livekit.TrackSource_SCREEN_SHARE
	pushed := 0
	pushOK := func() error { pushed++; return nil }

	off := toggles.begin(share)
	stale, err := toggles.push(share, off, func() error { pushed++; return errors.New("not linked") })
	require.False(t, stale)
	require.Error(t, err)

	on := toggles.begin(share)
	stale, err = toggles.push(share, off, pushOK)
	require.True(t, stale, "a retry of a replaced toggle is stale")
	require.NoError(t, err)
	require.Equal(t, 1, pushed, "a stale toggle is not pushed")

	toggles.begin(livekit.TrackSource_CAMERA)
	stale, err = toggles.push(share, on, pushOK)
	require.False(t, stale, "toggles of other tracks do not replace it")
	require.NoError(t, err)
	require.Equal(t, 2, pushed)
}
