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
	"context"
	"errors"
	"time"

	"github.com/livekit/psrpc"

	meetapi "github.com/livekit/sip/pkg/meet"
	"github.com/livekit/sip/pkg/sip/lobby"
	"github.com/livekit/sip/pkg/stats"
)

const (
	lobbyUnavailableAfter = 20 * time.Second
	lobbyDeniedHold       = 5 * time.Second

	attrMeetLobby = "meet.lobby"
	attrMeetColor = "color"
)

// meetJoin resolves pin with Meet and reports whether the caller must wait in the lobby.
// It fails only when the lobby is enforced and Meet could not answer.
func (c *inboundCall) meetJoin(ctx context.Context, pin string) (lobbyRequired bool, _ error) {
	m := c.s.meet
	if m == nil || pin == "" {
		return false, nil
	}
	enforced := c.s.conf.Meet.LobbyEnabled
	start := time.Now()
	res, err := m.Join(ctx, pin)
	switch {
	case err == nil:
		c.log().Infow("meet join succeeded", "pin", pin, "lobby", res.Lobby, "accessLevel", res.AccessLevel, "duration", time.Since(start))
		return enforced && res.Lobby == meetapi.LobbyRequired, nil
	case errors.Is(err, meetapi.ErrNotFound):
		c.log().Infow("meet knows no room for this pin", "pin", pin, "duration", time.Since(start))
		return false, nil
	case !enforced:
		c.log().Warnw("meet join failed, continuing to dispatch", err, "pin", pin, "duration", time.Since(start))
		return false, nil
	}
	c.log().Warnw("meet join failed, lobby cannot be checked", err, "pin", pin, "duration", time.Since(start))
	return false, err
}

// lobbyUI draws the lobby screens and plays the lobby prompts of a call.
type lobbyUI struct {
	c      *inboundCall
	medias *MediaOrchestrator
	scr    promptScreens
}

func (u lobbyUI) Show(v lobby.View) {
	u.medias.ShowScreen(u.scr.lobby(v))
}

func (u lobbyUI) Play(ctx context.Context, p lobby.Prompt) {
	if err := u.medias.PlayAudio(ctx, u.c.s.res.lobbyFd(p)); err != nil && ctx.Err() == nil {
		u.c.log().Errorw("Cannot play audio", err)
	}
}

// lobbyUsername is the name the organiser sees, the one the participant joins with.
func (c *inboundCall) lobbyUsername(disp *CallDispatch) string {
	if c.cc.From().User == "" {
		if _, name := sipFallbackParticipant(c.cc.invite); name != "" {
			return name
		}
	}
	if name := disp.Room.Participant.Name; name != "" {
		return name
	}
	return disp.Room.Participant.Identity
}

// lobbyWait keeps the caller in the Meet lobby until the organiser decides.
// ok is false when the call was closed instead of being let in.
func (c *inboundCall) lobbyWait(ctx context.Context, scr promptScreens, pin string, disp *CallDispatch) (ok bool, _ error) {
	ctx, span := Tracer.Start(ctx, "sip.inbound.lobbyWait")
	defer span.End()

	medias := c.medias
	if medias == nil {
		c.closeWithHangup(ctx)
		return false, nil
	}

	keys := make(chan byte, cap(c.dtmf))
	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			select {
			case <-done:
				return
			case ev := <-c.dtmf:
				select {
				case keys <- ev.Digit:
				default:
				}
			}
		}
	}()

	from := c.cc.From()
	caller := lobby.Caller{
		Pin:      pin,
		Username: c.lobbyUsername(disp),
		SIPURI:   from.String(),
	}
	if h := c.cc.invite.GetHeader("User-Agent"); h != nil {
		caller.UserAgent = h.Value()
	}
	conf := lobby.Config{
		PollInterval:     c.s.conf.Meet.LobbyPollInterval,
		Timeout:          c.s.conf.Meet.LobbyTimeout,
		RetryWindow:      c.s.conf.Meet.LobbyRetryWindow,
		UnavailableAfter: lobbyUnavailableAfter,
		DeniedHold:       lobbyDeniedHold,
		CancelTimeout:    c.s.conf.Meet.Timeout,
	}
	events := lobby.Events{
		Keys:        keys,
		Cancelled:   c.cc.Cancelled(),
		MediaClosed: medias.Closed(),
	}

	res := lobby.Run(ctx, conf, c.s.meet, lobbyUI{c: c, medias: medias, scr: scr}, events, caller, c.log())
	switch res.Outcome {
	case lobby.Accepted:
		p := &disp.Room.Participant
		if p.Attributes == nil {
			p.Attributes = make(map[string]string)
		}
		p.Attributes[attrMeetLobby] = "accepted"
		if res.Color != "" {
			p.Attributes[attrMeetColor] = res.Color
		}
		return true, nil
	case lobby.Denied:
		c.close(ctx, callDropped, stats.ClientError("lobby-denied"))
		return false, psrpc.NewErrorf(psrpc.PermissionDenied, "entry denied by the organiser")
	case lobby.NoAnswer:
		c.close(ctx, callDropped, stats.ClientError("lobby-timeout"))
		return false, psrpc.NewErrorf(psrpc.DeadlineExceeded, "nobody answered the entry request")
	case lobby.Cancelled:
		c.closeWithCancelled(ctx)
		return false, nil
	case lobby.HungUp:
		c.closeWithHangup(ctx)
		return false, nil
	case lobby.MediaClosed:
		c.close(ctx, callDropped, stats.ServerError("media-closed"))
		return false, psrpc.NewErrorf(psrpc.Canceled, "media closed while waiting in the lobby")
	}
	c.close(ctx, callDropped, stats.ServerError("lobby-unavailable"))
	return false, psrpc.NewErrorf(psrpc.Unavailable, "meet lobby unavailable")
}
