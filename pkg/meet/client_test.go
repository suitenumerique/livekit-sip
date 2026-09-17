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

package meet

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/livekit/sip/pkg/config"
)

func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return NewClient(config.MeetConfig{
		JoinURL:   srv.URL + "/api/v1.0/roomkit/join/",
		AuthToken: "secret",
		Timeout:   time.Second,
	})
}

func TestNewClientDisabled(t *testing.T) {
	require.Nil(t, NewClient(config.MeetConfig{}))
}

func TestSiblingURL(t *testing.T) {
	require.Equal(t, "https://meet.example/api/v1.0/roomkit/request-entry/",
		siblingURL("https://meet.example/api/v1.0/roomkit/join/", "request-entry"))
	require.Equal(t, "https://meet.example/api/v1.0/roomkit/cancel-entry",
		siblingURL("https://meet.example/api/v1.0/roomkit/join", "cancel-entry"))
	require.Equal(t, "http://localhost:8071/x/request-entry/?a=1",
		siblingURL("http://localhost:8071/x/join/?a=1", "request-entry"))
}

func TestExplicitURLsAreKept(t *testing.T) {
	c := NewClient(config.MeetConfig{
		JoinURL:         "https://a.example/join/",
		RequestEntryURL: "https://b.example/ask/",
		CancelEntryURL:  "https://c.example/drop/",
	})
	require.Equal(t, "https://b.example/ask/", c.requestEntryURL)
	require.Equal(t, "https://c.example/drop/", c.cancelEntryURL)
}

func TestJoin(t *testing.T) {
	var got map[string]any
	c := newTestClient(t, func(w http.ResponseWriter, req *http.Request) {
		require.Equal(t, http.MethodPost, req.Method)
		require.Equal(t, "/api/v1.0/roomkit/join/", req.URL.Path)
		require.Equal(t, "application/json", req.Header.Get("Content-Type"))
		require.Equal(t, "Bearer secret", req.Header.Get("Authorization"))
		require.NoError(t, json.NewDecoder(req.Body).Decode(&got))
		_, _ = w.Write([]byte(`{"status":"success","room":{"id":"r1","name":"Comité"},"access_level":"restricted","lobby":"required"}`))
	})
	res, err := c.Join(context.Background(), "1067684307")
	require.NoError(t, err)
	require.Equal(t, map[string]any{"pin_code": "1067684307"}, got)
	require.Equal(t, JoinResult{Lobby: LobbyRequired, AccessLevel: "restricted", RoomID: "r1", RoomName: "Comité"}, res)
}

func TestJoinWithoutLobbyField(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, req *http.Request) {
		_, _ = w.Write([]byte(`{"status":"success"}`))
	})
	res, err := c.Join(context.Background(), "1067684307")
	require.NoError(t, err)
	require.Empty(t, res.Lobby)

	c = newTestClient(t, func(w http.ResponseWriter, req *http.Request) {})
	res, err = c.Join(context.Background(), "1067684307")
	require.NoError(t, err)
	require.Empty(t, res.Lobby)
}

func TestJoinUnknownPin(t *testing.T) {
	for _, code := range []int{http.StatusNotFound, http.StatusBadRequest} {
		c := newTestClient(t, func(w http.ResponseWriter, req *http.Request) {
			w.WriteHeader(code)
		})
		_, err := c.Join(context.Background(), "12")
		require.ErrorIs(t, err, ErrNotFound)
	}
}

func TestJoinFailures(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"detail":"Could not create dispatch rule."}`))
	})
	_, err := c.Join(context.Background(), "1067684307")
	var se *StatusError
	require.ErrorAs(t, err, &se)
	require.Equal(t, http.StatusInternalServerError, se.Code)
	require.Contains(t, se.Body, "dispatch rule")

	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		time.Sleep(500 * time.Millisecond)
	}))
	defer slow.Close()
	c = NewClient(config.MeetConfig{JoinURL: slow.URL, Timeout: 50 * time.Millisecond})
	start := time.Now()
	_, err = c.Join(context.Background(), "1067684307")
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrNotFound)
	require.Less(t, time.Since(start), 400*time.Millisecond)
}

func TestRequestEntry(t *testing.T) {
	var got map[string]any
	reply := `{"status":"waiting","participant_id":"p1","admins_present":false,"waiting_since":"2026-09-14T11:13:02Z"}`
	c := newTestClient(t, func(w http.ResponseWriter, req *http.Request) {
		require.Equal(t, "/api/v1.0/roomkit/request-entry/", req.URL.Path)
		require.Equal(t, "Bearer secret", req.Header.Get("Authorization"))
		got = nil
		require.NoError(t, json.NewDecoder(req.Body).Decode(&got))
		_, _ = w.Write([]byte(reply))
	})

	st, err := c.RequestEntry(context.Background(), EntryRequest{
		PinCode: "1067684307", ParticipantID: "p1", Username: "StudioX30",
		SIPURI: "sip:salle@exemple.fr", UserAgent: "Poly Studio X30",
	})
	require.NoError(t, err)
	require.Equal(t, map[string]any{
		"pin_code": "1067684307", "participant_id": "p1", "username": "StudioX30",
		"sip_uri": "sip:salle@exemple.fr", "user_agent": "Poly Studio X30",
	}, got)
	require.Equal(t, EntryWaiting, st.Status)
	require.NotNil(t, st.AdminsPresent)
	require.False(t, *st.AdminsPresent)

	reply = `{"status":"accepted","participant_id":"p1","color":"hsl(212, 61%, 44%)"}`
	st, err = c.RequestEntry(context.Background(), EntryRequest{
		PinCode: "1067684307", ParticipantID: "p1", Username: strings.Repeat("é", 200),
	})
	require.NoError(t, err)
	require.NotContains(t, got, "sip_uri")
	require.NotContains(t, got, "user_agent")
	require.Len(t, []rune(got["username"].(string)), 128)
	require.Equal(t, EntryAccepted, st.Status)
	require.Equal(t, "hsl(212, 61%, 44%)", st.Color)
	require.Nil(t, st.AdminsPresent)

	reply = `{"status":"denied","participant_id":"p1"}`
	st, err = c.RequestEntry(context.Background(), EntryRequest{PinCode: "1067684307", ParticipantID: "p1", Username: "x"})
	require.NoError(t, err)
	require.Equal(t, EntryDenied, st.Status)
}

func TestCancelEntry(t *testing.T) {
	var got map[string]any
	c := newTestClient(t, func(w http.ResponseWriter, req *http.Request) {
		require.Equal(t, "/api/v1.0/roomkit/cancel-entry/", req.URL.Path)
		require.NoError(t, json.NewDecoder(req.Body).Decode(&got))
		w.WriteHeader(http.StatusNoContent)
	})
	require.NoError(t, c.CancelEntry(context.Background(), "1067684307", "p1"))
	require.Equal(t, map[string]any{"pin_code": "1067684307", "participant_id": "p1"}, got)
}
