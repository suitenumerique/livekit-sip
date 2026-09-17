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
	"github.com/livekit/sip/pkg/sip/lobby"
	lkc "github.com/livekit/sip/pkg/sip/pipeline/elements/livekitcompositor"
)

// promptScreens builds the localized code entry screens shown to the caller.
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
		Footer: []lkc.ScreenHint{
			{Key: "#", Label: s.p.Sprintf("Confirm")},
			{Key: "*", Label: s.p.Sprintf("Delete")},
			{Label: s.p.Sprintf("The code is in the invitation")},
		},
	}
}

func (s promptScreens) codeRejected(entered string, attempt int) lkc.Screen {
	attempts := s.attempts
	return lkc.Screen{
		Eyebrow:     s.p.Sprintf("Code not recognised"),
		EyebrowTone: lkc.ToneError,
		Title:       s.p.Sprintf("No meeting matches this code"),
		Body:        s.p.Sprintf("Attempt %d of %d. Check the code in the invitation, then try again.", attempt, attempts),
		Digits:      &lkc.ScreenDigits{Entered: entered, Length: s.length, Error: true},
		Footer:      []lkc.ScreenHint{{Key: "*", Label: s.p.Sprintf("Clear and start over")}},
	}
}

func (s promptScreens) connecting() lkc.Screen {
	return lkc.Screen{
		Eyebrow: s.p.Sprintf("Code accepted"),
		Title:   s.p.Sprintf("Connecting to the meeting…"),
	}
}

func (s promptScreens) timedOut(seconds int) lkc.Screen {
	return lkc.Screen{
		Icon:     lkc.IconClock,
		IconTone: lkc.ToneMuted,
		Eyebrow:  s.p.Sprintf("Time is up"),
		Title:    s.p.Sprintf("No key pressed for %d seconds", seconds),
		Body:     s.p.Sprintf("The call will end. Call again to retry."),
	}
}

func (s promptScreens) lobby(v lobby.View) lkc.Screen {
	hangUp := []lkc.ScreenHint{{Label: s.p.Sprintf("Hang up to cancel the request")}}
	switch v {
	case lobby.ViewNoAdmin:
		return lkc.Screen{
			Icon:     lkc.IconPerson,
			IconTone: lkc.ToneMuted,
			Eyebrow:  s.p.Sprintf("Restricted meeting"),
			Title:    s.p.Sprintf("The organiser has not arrived yet"),
			Body:     s.p.Sprintf("Your request will be shown to them as soon as they arrive."),
			Footer:   hangUp,
		}
	case lobby.ViewAccepted:
		return lkc.Screen{
			Icon:     lkc.IconCheck,
			IconTone: lkc.ToneSuccess,
			Eyebrow:  s.p.Sprintf("Request accepted"),
			Title:    s.p.Sprintf("Entering the meeting…"),
		}
	case lobby.ViewDenied:
		return lkc.Screen{
			Icon:        lkc.IconCross,
			IconTone:    lkc.ToneError,
			Eyebrow:     s.p.Sprintf("Request declined"),
			EyebrowTone: lkc.ToneError,
			Title:       s.p.Sprintf("You cannot join this call"),
			Body:        s.p.Sprintf("Your request to join was declined. The call will end."),
		}
	case lobby.ViewNoAnswer:
		return lkc.Screen{
			Icon:     lkc.IconHourglass,
			IconTone: lkc.ToneMuted,
			Eyebrow:  s.p.Sprintf("Restricted meeting"),
			Title:    s.p.Sprintf("Nobody answered"),
			Body:     s.p.Sprintf("You can send the request again, or hang up."),
			Footer:   []lkc.ScreenHint{{Key: "1", Label: s.p.Sprintf("Send the request again")}},
		}
	}
	return lkc.Screen{
		Icon:    lkc.IconHourglass,
		Eyebrow: s.p.Sprintf("Restricted meeting"),
		Title:   s.p.Sprintf("Request sent to the organiser"),
		Body:    s.p.Sprintf("You will enter the meeting as soon as someone present accepts your request."),
		Footer:  hangUp,
	}
}
