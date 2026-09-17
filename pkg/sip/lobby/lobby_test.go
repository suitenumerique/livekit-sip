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

package lobby

import (
	"context"
	"errors"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/livekit/protocol/logger"
	"github.com/stretchr/testify/require"

	"github.com/livekit/sip/pkg/meet"
)

var testConf = Config{
	PollInterval:     5 * time.Millisecond,
	Timeout:          2 * time.Second,
	RetryWindow:      2 * time.Second,
	UnavailableAfter: 40 * time.Millisecond,
	DeniedHold:       30 * time.Millisecond,
	CancelTimeout:    time.Second,
}

var testCaller = Caller{Pin: "1067684307", Username: "StudioX30", SIPURI: "sip:salle@exemple.fr", UserAgent: "Poly"}

type fakeBackend struct {
	mu       sync.Mutex
	requests []meet.EntryRequest
	cancels  []string
	respond  func(n int, req meet.EntryRequest) (meet.EntryStatus, error)
}

func (b *fakeBackend) RequestEntry(ctx context.Context, req meet.EntryRequest) (meet.EntryStatus, error) {
	b.mu.Lock()
	b.requests = append(b.requests, req)
	n := len(b.requests)
	b.mu.Unlock()
	return b.respond(n, req)
}

func (b *fakeBackend) CancelEntry(ctx context.Context, pin, participantID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.cancels = append(b.cancels, participantID)
	return nil
}

func (b *fakeBackend) ids() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var ids []string
	for _, r := range b.requests {
		if len(ids) == 0 || ids[len(ids)-1] != r.ParticipantID {
			ids = append(ids, r.ParticipantID)
		}
	}
	return ids
}

type fakeUI struct {
	mu      sync.Mutex
	views   []View
	prompts []Prompt
	shown   chan View
}

func newFakeUI() *fakeUI { return &fakeUI{shown: make(chan View, 32)} }

func (u *fakeUI) Show(v View) {
	u.mu.Lock()
	u.views = append(u.views, v)
	u.mu.Unlock()
	u.shown <- v
}

func (u *fakeUI) Play(ctx context.Context, p Prompt) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.prompts = append(u.prompts, p)
}

func (u *fakeUI) waitFor(t *testing.T, want View) {
	t.Helper()
	timeout := time.After(2 * time.Second)
	for {
		select {
		case v := <-u.shown:
			if v == want {
				return
			}
		case <-timeout:
			t.Fatalf("view %d never shown", want)
		}
	}
}

func waiting() (meet.EntryStatus, error) { return meet.EntryStatus{Status: meet.EntryWaiting}, nil }

func TestAccepted(t *testing.T) {
	b := &fakeBackend{respond: func(n int, req meet.EntryRequest) (meet.EntryStatus, error) {
		if n < 3 {
			return waiting()
		}
		return meet.EntryStatus{Status: meet.EntryAccepted, Color: "hsl(212, 61%, 44%)"}, nil
	}}
	ui := newFakeUI()
	res := Run(context.Background(), testConf, b, ui, Events{}, testCaller, logger.GetLogger())

	require.Equal(t, Result{Outcome: Accepted, Color: "hsl(212, 61%, 44%)"}, res)
	require.Equal(t, []View{ViewWaiting, ViewAccepted}, ui.views)
	require.Equal(t, []Prompt{PromptWaiting, PromptAccepted}, ui.prompts)
	require.Empty(t, b.cancels)
	require.Len(t, b.ids(), 1)
	require.Regexp(t, regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`), b.ids()[0])
	first := b.requests[0]
	first.ParticipantID = ""
	require.Equal(t, meet.EntryRequest{
		PinCode: "1067684307", Username: "StudioX30", SIPURI: "sip:salle@exemple.fr", UserAgent: "Poly",
	}, first)
}

func TestDenied(t *testing.T) {
	b := &fakeBackend{respond: func(n int, req meet.EntryRequest) (meet.EntryStatus, error) {
		return meet.EntryStatus{Status: meet.EntryDenied}, nil
	}}
	ui := newFakeUI()
	start := time.Now()
	res := Run(context.Background(), testConf, b, ui, Events{}, testCaller, logger.GetLogger())

	require.Equal(t, Denied, res.Outcome)
	require.GreaterOrEqual(t, time.Since(start), testConf.DeniedHold)
	require.Equal(t, []View{ViewWaiting, ViewDenied}, ui.views)
	require.Equal(t, []Prompt{PromptWaiting, PromptDenied}, ui.prompts)
	require.Empty(t, b.cancels)
}

func TestNoAnswerThenRetry(t *testing.T) {
	conf := testConf
	conf.Timeout = 30 * time.Millisecond
	var retried bool
	var mu sync.Mutex
	b := &fakeBackend{respond: func(n int, req meet.EntryRequest) (meet.EntryStatus, error) {
		mu.Lock()
		defer mu.Unlock()
		if retried {
			return meet.EntryStatus{Status: meet.EntryAccepted}, nil
		}
		return waiting()
	}}
	ui := newFakeUI()
	keys := make(chan byte, 4)
	go func() {
		ui.waitFor(t, ViewNoAnswer)
		mu.Lock()
		retried = true
		mu.Unlock()
		keys <- '5'
		keys <- '1'
	}()
	res := Run(context.Background(), conf, b, ui, Events{Keys: keys}, testCaller, logger.GetLogger())

	require.Equal(t, Accepted, res.Outcome)
	require.Equal(t, []View{ViewWaiting, ViewNoAnswer, ViewWaiting, ViewAccepted}, ui.views)
	ids := b.ids()
	require.Len(t, ids, 2)
	require.NotEqual(t, ids[0], ids[1])
	require.Equal(t, []string{ids[0]}, b.cancels)
}

func TestNoAnswerGivesUp(t *testing.T) {
	conf := testConf
	conf.Timeout = 20 * time.Millisecond
	conf.RetryWindow = 30 * time.Millisecond
	b := &fakeBackend{respond: func(n int, req meet.EntryRequest) (meet.EntryStatus, error) { return waiting() }}
	ui := newFakeUI()
	res := Run(context.Background(), conf, b, ui, Events{}, testCaller, logger.GetLogger())

	require.Equal(t, NoAnswer, res.Outcome)
	require.Equal(t, []View{ViewWaiting, ViewNoAnswer}, ui.views)
	require.Equal(t, b.ids(), b.cancels)
}

func TestHangupCancelsTheRequest(t *testing.T) {
	ctx, hangup := context.WithCancel(context.Background())
	b := &fakeBackend{respond: func(n int, req meet.EntryRequest) (meet.EntryStatus, error) {
		if n == 3 {
			hangup()
		}
		return waiting()
	}}
	ui := newFakeUI()
	res := Run(ctx, testConf, b, ui, Events{}, testCaller, logger.GetLogger())

	require.Equal(t, HungUp, res.Outcome)
	require.Equal(t, b.ids(), b.cancels)
	require.Equal(t, []View{ViewWaiting}, ui.views)
}

func TestCallEventsEndTheWait(t *testing.T) {
	for _, tc := range []struct {
		name string
		want Outcome
		ev   func(ch chan struct{}) Events
	}{
		{"cancelled", Cancelled, func(ch chan struct{}) Events { return Events{Cancelled: ch} }},
		{"media closed", MediaClosed, func(ch chan struct{}) Events { return Events{MediaClosed: ch} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ch := make(chan struct{})
			b := &fakeBackend{respond: func(n int, req meet.EntryRequest) (meet.EntryStatus, error) {
				if n == 2 {
					close(ch)
				}
				return waiting()
			}}
			res := Run(context.Background(), testConf, b, newFakeUI(), tc.ev(ch), testCaller, logger.GetLogger())
			require.Equal(t, tc.want, res.Outcome)
			require.Equal(t, b.ids(), b.cancels)
		})
	}
}

func TestUnavailable(t *testing.T) {
	b := &fakeBackend{respond: func(n int, req meet.EntryRequest) (meet.EntryStatus, error) {
		return meet.EntryStatus{}, errors.New("connection refused")
	}}
	ui := newFakeUI()
	start := time.Now()
	res := Run(context.Background(), testConf, b, ui, Events{}, testCaller, logger.GetLogger())
	require.Equal(t, Unavailable, res.Outcome)
	require.GreaterOrEqual(t, time.Since(start), testConf.UnavailableAfter)
	require.Equal(t, []View{ViewWaiting}, ui.views)

	b = &fakeBackend{respond: func(n int, req meet.EntryRequest) (meet.EntryStatus, error) {
		return meet.EntryStatus{}, meet.ErrNotFound
	}}
	res = Run(context.Background(), testConf, b, newFakeUI(), Events{}, testCaller, logger.GetLogger())
	require.Equal(t, Unavailable, res.Outcome)
	require.Len(t, b.requests, 1)
}

func TestFailuresAreForgivenAfterASuccess(t *testing.T) {
	b := &fakeBackend{respond: func(n int, req meet.EntryRequest) (meet.EntryStatus, error) {
		switch {
		case n > 40:
			return meet.EntryStatus{Status: meet.EntryAccepted}, nil
		case n%3 == 0:
			return waiting()
		}
		return meet.EntryStatus{}, errors.New("timeout")
	}}
	res := Run(context.Background(), testConf, b, newFakeUI(), Events{}, testCaller, logger.GetLogger())
	require.Equal(t, Accepted, res.Outcome)
}

func TestNoAdminView(t *testing.T) {
	absent, present := false, true
	b := &fakeBackend{respond: func(n int, req meet.EntryRequest) (meet.EntryStatus, error) {
		switch {
		case n <= 2:
			return meet.EntryStatus{Status: meet.EntryWaiting, AdminsPresent: &absent}, nil
		case n <= 4:
			return meet.EntryStatus{Status: meet.EntryWaiting, AdminsPresent: &present}, nil
		case n <= 6:
			return meet.EntryStatus{Status: meet.EntryWaiting, AdminsPresent: &absent}, nil
		}
		return meet.EntryStatus{Status: meet.EntryAccepted}, nil
	}}
	ui := newFakeUI()
	res := Run(context.Background(), testConf, b, ui, Events{}, testCaller, logger.GetLogger())

	require.Equal(t, Accepted, res.Outcome)
	require.Equal(t, []View{ViewWaiting, ViewNoAdmin, ViewWaiting, ViewNoAdmin, ViewAccepted}, ui.views)
	require.Equal(t, []Prompt{PromptWaiting, PromptNoAdmin, PromptAccepted}, ui.prompts)
}
