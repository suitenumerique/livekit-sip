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
	"golang.org/x/text/message"

	"github.com/livekit/sip/pkg/i18n"
	lkc "github.com/livekit/sip/pkg/sip/pipeline/elements/livekitcompositor"
)

type promptScreens struct {
	p        *message.Printer
	length   int
	attempts int
}

func newPromptScreens(lang string, length, attempts int) promptScreens {
	return promptScreens{p: i18n.Printer(lang), length: length, attempts: attempts}
}

func (s promptScreens) codeEntry(entered string) lkc.Screen {
	return lkc.Screen{
		Eyebrow: s.p.Sprintf("Join a meeting"),
		Title:   s.p.Sprintf("Enter the meeting code, then #"),
		Digits:  &lkc.ScreenDigits{Entered: entered, Length: s.length},
		Footer:  []lkc.ScreenHint{{Key: "*", Label: s.p.Sprintf("Delete")}},
	}
}

func (s promptScreens) codeRejected(entered string, attempt int) lkc.Screen {
	attempts := s.attempts
	screen := lkc.Screen{
		Tone:    lkc.ToneError,
		Eyebrow: s.p.Sprintf("Join a meeting"),
		Title:   s.p.Sprintf("Invalid code"),
		Digits:  &lkc.ScreenDigits{Entered: entered, Length: s.length, Error: true},
	}
	if attempt >= attempts {
		screen.Body = s.p.Sprintf("Attempt: %d/%d. The call will end.", attempt, attempts)
		return screen
	}
	screen.Body = s.p.Sprintf("Attempt: %d/%d. Check the code in the invitation, then try again.", attempt, attempts)
	screen.Footer = []lkc.ScreenHint{{Key: "*", Label: s.p.Sprintf("Start over")}}
	return screen
}

func (s promptScreens) connecting() lkc.Screen {
	return lkc.Screen{
		Spinner: true,
		Caption: s.p.Sprintf("Connecting to the meeting"),
	}
}

func (s promptScreens) timedOut(seconds int) lkc.Screen {
	return lkc.Screen{
		Tone:  lkc.ToneError,
		Icon:  lkc.IconClock,
		Title: s.p.Sprintf("Time is up"),
		Body:  s.p.Sprintf("No key pressed for %d seconds, the call will end.", seconds),
	}
}
