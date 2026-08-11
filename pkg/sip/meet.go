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
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/livekit/protocol/logger"

	"github.com/livekit/sip/pkg/config"
)

// meetJoinLogBodyLimit caps how much of the Meet response body is logged.
const meetJoinLogBodyLimit = 2 << 10

// meetClient calls the Meet backend (roomkit join API) to create the LiveKit
// dispatch rule for a pin code before the dispatch rule evaluation runs.
// Without it, a SIP device joining first with a valid pin is rejected because
// the rule only exists once a participant has joined the room.
type meetClient struct {
	url     string
	token   string
	timeout time.Duration
	client  *http.Client
}

// newMeetClient returns nil when no join URL is configured, disabling the feature.
func newMeetClient(conf config.MeetConfig) *meetClient {
	if conf.JoinURL == "" {
		return nil
	}
	return &meetClient{
		url:     conf.JoinURL,
		token:   conf.AuthToken,
		timeout: conf.Timeout,
		client:  &http.Client{Timeout: conf.Timeout},
	}
}

// preCreateDispatchRule posts the pin code to the Meet join endpoint.
// Best-effort: failures are logged and the caller proceeds with the dispatch
// rule evaluation regardless. No-op on a nil client or an empty pin.
func (m *meetClient) preCreateDispatchRule(ctx context.Context, log logger.Logger, pin string) {
	if m == nil || pin == "" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()

	body, err := json.Marshal(struct {
		PinCode string `json:"pin_code"`
	}{PinCode: pin})
	if err != nil {
		log.Warnw("meet join failed, continuing to dispatch", err, "pin", pin)
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.url, bytes.NewReader(body))
	if err != nil {
		log.Warnw("meet join failed, continuing to dispatch", err, "pin", pin)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if m.token != "" {
		req.Header.Set("Authorization", "Bearer "+m.token)
	}

	log.Debugw("meet join request", "url", m.url, "pin", pin)
	start := time.Now()
	resp, err := m.client.Do(req)
	if err != nil {
		log.Warnw("meet join failed, continuing to dispatch", err, "pin", pin, "url", m.url, "duration", time.Since(start))
		return
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, meetJoinLogBodyLimit))
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Warnw("meet join failed, continuing to dispatch", nil, "pin", pin, "url", m.url,
			"status", resp.StatusCode, "response", string(respBody), "duration", time.Since(start))
		return
	}
	log.Infow("meet join succeeded", "pin", pin, "status", resp.StatusCode, "duration", time.Since(start))
	log.Debugw("meet join response", "pin", pin, "response", string(respBody))
}
