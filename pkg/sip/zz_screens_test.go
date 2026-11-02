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

	lkc "github.com/livekit/sip/pkg/sip/pipeline/elements/livekitcompositor"
)

func TestPromptScreens_CodeEntry(t *testing.T) {
	s := newPromptScreens("fr", 10, 3).codeEntry("123")
	require.Equal(t, "Rejoindre une réunion", s.Eyebrow)
	require.Equal(t, "Tapez le code de la réunion, puis #", s.Title)
	require.Equal(t, "123", s.Digits.Entered)
	require.Equal(t, 10, s.Digits.Length)
	require.False(t, s.Digits.Error)
	require.Equal(t, []lkc.ScreenHint{{Key: "*", Label: "Effacer"}}, s.Footer)
}

func TestPromptScreens_RejectedOffersARetryUntilTheLastAttempt(t *testing.T) {
	scr := newPromptScreens("fr", 10, 3)

	retry := scr.codeRejected("1234567809", 2)
	require.Equal(t, lkc.ToneError, retry.Tone)
	require.Equal(t, "Code invalide", retry.Title)
	require.Equal(t, "Essai : 2/3. Vérifiez le code dans l’invitation, puis recommencez.", retry.Body)
	require.True(t, retry.Digits.Error)
	require.Equal(t, []lkc.ScreenHint{{Key: "*", Label: "Recommencer"}}, retry.Footer)

	last := scr.codeRejected("1234567809", 3)
	require.Equal(t, "Essai : 3/3. L’appel va se terminer.", last.Body)
	require.Empty(t, last.Footer)
}

func TestPromptScreens_TimedOutAndConnecting(t *testing.T) {
	scr := newPromptScreens("fr", 10, 3)

	timedOut := scr.timedOut(60)
	require.Equal(t, lkc.IconClock, timedOut.Icon)
	require.Equal(t, "Délai dépassé", timedOut.Title)
	require.Equal(t, "Aucune saisie depuis 60 secondes, l’appel va se terminer.", timedOut.Body)

	connecting := scr.connecting()
	require.True(t, connecting.Spinner)
	require.Equal(t, "Connexion à la réunion", connecting.Caption)
}

func TestPromptScreens_English(t *testing.T) {
	scr := newPromptScreens("en", 10, 3)
	require.Equal(t, "Enter the meeting code, then #", scr.codeEntry("").Title)
	require.Equal(t, "Attempt: 3/3. The call will end.", scr.codeRejected("", 3).Body)
}
