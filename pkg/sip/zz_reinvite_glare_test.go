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
	"testing"
	"time"

	"github.com/livekit/sipgo/sip"
	"github.com/stretchr/testify/require"
)

func TestReinviteGlareAnswers491(t *testing.T) {
	st := NewServiceTest(t, nil)
	pinDispatch(st)
	call, ic := st.CreateInboundCall(t)
	t.Cleanup(func() { ic.Close() })
	waitEstablished(t, ic)

	sink := st.TestUA.RegisterSink(call.localTag, "INVITE")
	defer st.TestUA.UnregisterSink(call.localTag, "INVITE")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	ours := make(chan *sip.Response, 1)
	go func() {
		resp, err := ic.cc.SendReInvite(ctx, call.remoteSDP)
		if err != nil {
			t.Errorf("our re-INVITE: %v", err)
		}
		ours <- resp
	}()
	var pending *sipUARequest
	select {
	case pending = <-sink:
	case <-ctx.Done():
		t.Fatal("our re-INVITE never reached the device")
	}
	require.True(t, ic.cc.reinviteInFlight.Load())

	req, _, err := call.Invite(call.localSDP)
	require.NoError(t, err)
	resp := st.TestUA.TransactionRequest(t, req, true)
	require.Equal(t, statusRequestPending, resp.StatusCode, "the device re-INVITE crossing ours gets 491")

	answer := sip.NewResponseFromRequest(pending.req, 200, "OK", call.localSDP)
	answer.AppendHeader(&contentTypeHeaderSDP)
	require.NoError(t, pending.tx.Respond(answer))
	select {
	case resp := <-ours:
		require.NotNil(t, resp)
		require.Equal(t, sip.StatusCode(200), resp.StatusCode)
	case <-ctx.Done():
		t.Fatal("our re-INVITE never completed")
	}
	require.False(t, ic.cc.reinviteInFlight.Load())

	req, _, err = call.Invite(call.localSDP)
	require.NoError(t, err)
	resp = st.TestUA.TransactionRequest(t, req, true)
	require.Equal(t, sip.StatusCode(200), resp.StatusCode, "the device retry after the glare is accepted")
}
