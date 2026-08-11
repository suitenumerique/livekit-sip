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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/livekit/protocol/logger"
	"github.com/stretchr/testify/require"

	"github.com/livekit/sip/pkg/config"
)

func TestMeetClientDisabled(t *testing.T) {
	require.Nil(t, newMeetClient(config.MeetConfig{}))

	// Nil client and empty pin are both no-ops.
	var m *meetClient
	m.preCreateDispatchRule(context.Background(), logger.GetLogger(), "1234")

	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		called = true
	}))
	defer srv.Close()
	m = newMeetClient(config.MeetConfig{JoinURL: srv.URL, Timeout: time.Second})
	m.preCreateDispatchRule(context.Background(), logger.GetLogger(), "")
	require.False(t, called)
}

func TestMeetClientJoin(t *testing.T) {
	var got struct {
		PinCode string `json:"pin_code"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		require.Equal(t, http.MethodPost, req.Method)
		require.Equal(t, "application/json", req.Header.Get("Content-Type"))
		require.Equal(t, "Bearer secret", req.Header.Get("Authorization"))
		require.NoError(t, json.NewDecoder(req.Body).Decode(&got))
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	m := newMeetClient(config.MeetConfig{
		JoinURL:   srv.URL,
		AuthToken: "secret",
		Timeout:   time.Second,
	})
	m.preCreateDispatchRule(context.Background(), logger.GetLogger(), "1067684307")
	require.Equal(t, "1067684307", got.PinCode)
}

func TestMeetClientBestEffort(t *testing.T) {
	// Server errors and timeouts must return without panicking or hanging.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	m := newMeetClient(config.MeetConfig{JoinURL: srv.URL, Timeout: time.Second})
	m.preCreateDispatchRule(context.Background(), logger.GetLogger(), "1234")

	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		time.Sleep(500 * time.Millisecond)
	}))
	defer slow.Close()
	m = newMeetClient(config.MeetConfig{JoinURL: slow.URL, Timeout: 50 * time.Millisecond})
	start := time.Now()
	m.preCreateDispatchRule(context.Background(), logger.GetLogger(), "1234")
	require.Less(t, time.Since(start), 400*time.Millisecond)
}
