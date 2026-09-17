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

	"github.com/stretchr/testify/require"

	"github.com/livekit/sip/pkg/sip/lobby"
	lkc "github.com/livekit/sip/pkg/sip/pipeline/elements/livekitcompositor"
)

func TestPromptScreensLobby(t *testing.T) {
	scr := newPromptScreens("en", 10, 3)

	waiting := scr.lobby(lobby.ViewWaiting)
	require.Equal(t, lkc.IconHourglass, waiting.Icon)
	require.Equal(t, lkc.ToneAccent, waiting.IconTone)
	require.Equal(t, "Request sent to the organiser", waiting.Title)
	require.Equal(t, []lkc.ScreenHint{{Label: "Hang up to cancel the request"}}, waiting.Footer)

	noAdmin := scr.lobby(lobby.ViewNoAdmin)
	require.Equal(t, lkc.IconPerson, noAdmin.Icon)
	require.Equal(t, lkc.ToneMuted, noAdmin.IconTone)
	require.Equal(t, waiting.Footer, noAdmin.Footer)

	accepted := scr.lobby(lobby.ViewAccepted)
	require.Equal(t, lkc.IconCheck, accepted.Icon)
	require.Equal(t, lkc.ToneSuccess, accepted.IconTone)
	require.Empty(t, accepted.Footer)

	denied := scr.lobby(lobby.ViewDenied)
	require.Equal(t, lkc.IconCross, denied.Icon)
	require.Equal(t, lkc.ToneError, denied.IconTone)
	require.Equal(t, lkc.ToneError, denied.EyebrowTone)
	require.NotEmpty(t, denied.Body)

	noAnswer := scr.lobby(lobby.ViewNoAnswer)
	require.Equal(t, lkc.IconHourglass, noAnswer.Icon)
	require.Equal(t, lkc.ToneMuted, noAnswer.IconTone)
	require.Equal(t, []lkc.ScreenHint{{Key: "1", Label: "Send the request again"}}, noAnswer.Footer)

	for _, v := range []lobby.View{lobby.ViewWaiting, lobby.ViewNoAdmin, lobby.ViewAccepted, lobby.ViewDenied, lobby.ViewNoAnswer} {
		s := scr.lobby(v)
		require.NotEmpty(t, s.Eyebrow)
		require.NotEmpty(t, s.Title)
	}
}
