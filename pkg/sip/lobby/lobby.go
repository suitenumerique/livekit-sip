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

// Package lobby makes a SIP call wait in the Meet lobby of a restricted room.
package lobby

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/livekit/protocol/logger"

	"github.com/livekit/sip/pkg/meet"
)

// View is a screen of the lobby.
type View int

const (
	ViewWaiting View = iota
	ViewNoAdmin
	ViewAccepted
	ViewDenied
	ViewNoAnswer
)

// Prompt is an audio prompt of the lobby.
type Prompt int

const (
	PromptWaiting Prompt = iota
	PromptNoAdmin
	PromptAccepted
	PromptDenied
	PromptNoAnswer
)

// UI renders the lobby to the caller. Play returns once the prompt has been played.
type UI interface {
	Show(v View)
	Play(ctx context.Context, p Prompt)
}

// Backend is the part of the Meet client the lobby uses.
type Backend interface {
	RequestEntry(ctx context.Context, req meet.EntryRequest) (meet.EntryStatus, error)
	CancelEntry(ctx context.Context, pin, participantID string) error
}

// Events are the call events watched while waiting. A nil channel never fires.
type Events struct {
	Keys        <-chan byte
	Cancelled   <-chan struct{}
	MediaClosed <-chan struct{}
}

type Config struct {
	PollInterval     time.Duration // delay between two entry requests
	Timeout          time.Duration // wait before the no-answer screen
	RetryWindow      time.Duration // time to press 1 on the no-answer screen
	UnavailableAfter time.Duration // duration of consecutive request failures tolerated
	DeniedHold       time.Duration // time the denied screen stays up
	CancelTimeout    time.Duration // budget of the cancel request
}

// Caller identifies the SIP device to the organiser.
type Caller struct {
	Pin       string
	Username  string
	SIPURI    string
	UserAgent string
}

type Outcome int

const (
	Accepted Outcome = iota
	Denied
	NoAnswer
	Unavailable
	HungUp
	Cancelled
	MediaClosed
)

func (o Outcome) String() string {
	switch o {
	case Accepted:
		return "accepted"
	case Denied:
		return "denied"
	case NoAnswer:
		return "no-answer"
	case Unavailable:
		return "unavailable"
	case HungUp:
		return "hung-up"
	case Cancelled:
		return "cancelled"
	case MediaClosed:
		return "media-closed"
	}
	return fmt.Sprintf("outcome(%d)", int(o))
}

// Result is how the wait ended. Color is the avatar color Meet assigned on acceptance.
type Result struct {
	Outcome Outcome
	Color   string
}

type session struct {
	conf    Config
	backend Backend
	ui      UI
	ev      Events
	caller  Caller
	log     logger.Logger

	queue         chan Prompt    // background prompts, played in order
	playing       sync.WaitGroup // background prompts not played yet
	over          atomic.Bool
	noAdminPlayed bool
}

// Run asks Meet to let the caller in and blocks until the organiser decides,
// nobody answers, the backend stays unreachable or the call ends.
func Run(ctx context.Context, conf Config, backend Backend, ui UI, ev Events, caller Caller, log logger.Logger) Result {
	s := &session{conf: conf, backend: backend, ui: ui, ev: ev, caller: caller, log: log, queue: make(chan Prompt, 8)}
	go s.announce(ctx)
	defer s.stop()
	for {
		id := newParticipantID()
		res, again := s.attempt(ctx, id)
		if !again {
			s.log.Infow("lobby ended", "outcome", res.Outcome.String(), "participantID", id)
			return res
		}
	}
}

// attempt runs one entry request; again is true when the caller asked to send a new one.
func (s *session) attempt(ctx context.Context, id string) (res Result, again bool) {
	s.log.Infow("lobby entry requested", "participantID", id, "username", s.caller.Username)
	out, color := s.wait(ctx, id)
	switch out {
	case Accepted:
		s.ui.Show(ViewAccepted)
		s.playSync(ctx, PromptAccepted)
		return Result{Outcome: Accepted, Color: color}, false
	case Denied:
		s.ui.Show(ViewDenied)
		hold := time.NewTimer(s.conf.DeniedHold)
		defer hold.Stop()
		s.playSync(ctx, PromptDenied)
		select {
		case <-hold.C:
		case <-ctx.Done():
		}
		return Result{Outcome: Denied}, false
	case NoAnswer:
		s.cancel(ctx, id)
		return s.offerRetry(ctx)
	default:
		s.cancel(ctx, id)
		return Result{Outcome: out}, false
	}
}

// wait polls the entry request until it is decided, times out or the call ends.
func (s *session) wait(ctx context.Context, id string) (Outcome, string) {
	view := ViewWaiting
	s.ui.Show(view)
	s.playAsync(PromptWaiting)

	deadline := time.NewTimer(s.conf.Timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(s.conf.PollInterval)
	defer ticker.Stop()

	var failingSince time.Time
	for {
		st, err := s.backend.RequestEntry(ctx, meet.EntryRequest{
			PinCode:       s.caller.Pin,
			ParticipantID: id,
			Username:      s.caller.Username,
			SIPURI:        s.caller.SIPURI,
			UserAgent:     s.caller.UserAgent,
		})
		switch {
		case ctx.Err() != nil:
			return HungUp, ""
		case errors.Is(err, meet.ErrNotFound):
			s.log.Warnw("lobby entry request rejected", err, "participantID", id)
			return Unavailable, ""
		case err != nil:
			if failingSince.IsZero() {
				failingSince = time.Now()
			}
			s.log.Warnw("lobby entry request failed", err, "participantID", id, "failingFor", time.Since(failingSince))
			if time.Since(failingSince) >= s.conf.UnavailableAfter {
				return Unavailable, ""
			}
		default:
			failingSince = time.Time{}
			switch st.Status {
			case meet.EntryAccepted:
				return Accepted, st.Color
			case meet.EntryDenied:
				return Denied, ""
			}
			if want := viewFor(st); want != view {
				view = want
				s.ui.Show(view)
				if view == ViewNoAdmin && !s.noAdminPlayed {
					s.noAdminPlayed = true
					s.playAsync(PromptNoAdmin)
				}
			}
		}

		for polled := false; !polled; {
			select {
			case <-ticker.C:
				polled = true
			case <-s.ev.Keys:
			case <-deadline.C:
				return NoAnswer, ""
			case <-ctx.Done():
				return HungUp, ""
			case <-s.ev.Cancelled:
				return Cancelled, ""
			case <-s.ev.MediaClosed:
				return MediaClosed, ""
			}
		}
	}
}

func viewFor(st meet.EntryStatus) View {
	if st.AdminsPresent != nil && !*st.AdminsPresent {
		return ViewNoAdmin
	}
	return ViewWaiting
}

// offerRetry shows the no-answer screen and waits for key 1.
func (s *session) offerRetry(ctx context.Context) (Result, bool) {
	s.ui.Show(ViewNoAnswer)
	s.playAsync(PromptNoAnswer)
	window := time.NewTimer(s.conf.RetryWindow)
	defer window.Stop()
	for {
		select {
		case key := <-s.ev.Keys:
			if key == '1' {
				return Result{}, true
			}
		case <-window.C:
			return Result{Outcome: NoAnswer}, false
		case <-ctx.Done():
			return Result{Outcome: HungUp}, false
		case <-s.ev.Cancelled:
			return Result{Outcome: Cancelled}, false
		case <-s.ev.MediaClosed:
			return Result{Outcome: MediaClosed}, false
		}
	}
}

// cancel withdraws the entry request, even when ctx is already done.
func (s *session) cancel(ctx context.Context, id string) {
	ctx, stop := context.WithTimeout(context.WithoutCancel(ctx), s.conf.CancelTimeout)
	defer stop()
	if err := s.backend.CancelEntry(ctx, s.caller.Pin, id); err != nil {
		s.log.Infow("lobby entry cancel failed", "participantID", id, "error", err)
	}
}

// announce plays the queued prompts one after the other.
func (s *session) announce(ctx context.Context) {
	for p := range s.queue {
		if !s.over.Load() {
			s.ui.Play(ctx, p)
		}
		s.playing.Done()
	}
}

// stop drops the prompts still queued and ends announce.
func (s *session) stop() {
	s.over.Store(true)
	close(s.queue)
}

// playAsync queues p behind the background prompts already requested.
func (s *session) playAsync(p Prompt) {
	s.playing.Add(1)
	select {
	case s.queue <- p:
	default:
		s.playing.Done()
	}
}

// playSync plays p once the background prompts are over.
func (s *session) playSync(ctx context.Context, p Prompt) {
	s.playing.Wait()
	s.ui.Play(ctx, p)
}

// newParticipantID returns a random UUID (version 4).
func newParticipantID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
