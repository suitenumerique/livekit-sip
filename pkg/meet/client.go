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

// Package meet is a client of the Meet backend roomkit API.
package meet

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/livekit/sip/pkg/config"
)

// Lobby values of a join response.
const (
	LobbyBypass   = "bypass"
	LobbyRequired = "required"
)

// Statuses of a request-entry response.
const (
	EntryWaiting  = "waiting"
	EntryAccepted = "accepted"
	EntryDenied   = "denied"
)

const (
	bodyLimit     = 16 << 10
	usernameLimit = 128
)

// ErrNotFound reports that Meet knows no room for the pin code.
var ErrNotFound = errors.New("no room found for this pin code")

// StatusError is an unexpected HTTP status returned by Meet.
type StatusError struct {
	Code int
	Body string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("meet responded %d: %s", e.Code, e.Body)
}

// Client calls the roomkit endpoints with the server-to-server token.
type Client struct {
	joinURL         string
	requestEntryURL string
	cancelEntryURL  string
	token           string
	timeout         time.Duration
	http            *http.Client
}

// NewClient returns nil when no join URL is configured.
func NewClient(conf config.MeetConfig) *Client {
	if conf.JoinURL == "" {
		return nil
	}
	c := &Client{
		joinURL:         conf.JoinURL,
		requestEntryURL: conf.RequestEntryURL,
		cancelEntryURL:  conf.CancelEntryURL,
		token:           conf.AuthToken,
		timeout:         conf.Timeout,
		http:            &http.Client{Timeout: conf.Timeout},
	}
	if c.requestEntryURL == "" {
		c.requestEntryURL = siblingURL(conf.JoinURL, "request-entry")
	}
	if c.cancelEntryURL == "" {
		c.cancelEntryURL = siblingURL(conf.JoinURL, "cancel-entry")
	}
	return c
}

// siblingURL replaces the last path segment of rawURL with name.
func siblingURL(rawURL, name string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	trailing := strings.HasSuffix(u.Path, "/")
	p := strings.TrimSuffix(u.Path, "/")
	p = p[:strings.LastIndex(p, "/")+1] + name
	if trailing {
		p += "/"
	}
	u.Path = p
	return u.String()
}

// JoinResult is what Meet reports about the room behind a pin code.
// Lobby is empty when the backend does not report it.
type JoinResult struct {
	Lobby       string
	AccessLevel string
	RoomID      string
	RoomName    string
}

// Join resolves the pin code and has Meet create the SIP dispatch rule of the room.
func (c *Client) Join(ctx context.Context, pin string) (JoinResult, error) {
	in := struct {
		PinCode string `json:"pin_code"`
	}{PinCode: pin}
	var out struct {
		AccessLevel string `json:"access_level"`
		Lobby       string `json:"lobby"`
		Room        struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"room"`
	}
	err := c.post(ctx, c.joinURL, in, &out)
	var se *StatusError
	if errors.As(err, &se) && se.Code == http.StatusBadRequest {
		err = ErrNotFound
	}
	if err != nil {
		return JoinResult{}, err
	}
	return JoinResult{
		Lobby:       out.Lobby,
		AccessLevel: out.AccessLevel,
		RoomID:      out.Room.ID,
		RoomName:    out.Room.Name,
	}, nil
}

// EntryRequest asks to enter the room behind PinCode on behalf of a SIP device.
type EntryRequest struct {
	PinCode       string `json:"pin_code"`
	ParticipantID string `json:"participant_id"`
	Username      string `json:"username"`
	SIPURI        string `json:"sip_uri,omitempty"`
	UserAgent     string `json:"user_agent,omitempty"`
}

// EntryStatus is the state of an entry request.
// AdminsPresent is nil when the backend does not report it.
type EntryStatus struct {
	Status        string `json:"status"`
	ParticipantID string `json:"participant_id"`
	AdminsPresent *bool  `json:"admins_present"`
	Color         string `json:"color"`
}

// RequestEntry creates or refreshes the entry request and returns its state.
func (c *Client) RequestEntry(ctx context.Context, req EntryRequest) (EntryStatus, error) {
	if r := []rune(req.Username); len(r) > usernameLimit {
		req.Username = string(r[:usernameLimit])
	}
	var out EntryStatus
	if err := c.post(ctx, c.requestEntryURL, req, &out); err != nil {
		return EntryStatus{}, err
	}
	return out, nil
}

// CancelEntry withdraws the entry request.
func (c *Client) CancelEntry(ctx context.Context, pin, participantID string) error {
	in := struct {
		PinCode       string `json:"pin_code"`
		ParticipantID string `json:"participant_id"`
	}{PinCode: pin, ParticipantID: participantID}
	return c.post(ctx, c.cancelEntryURL, in, nil)
}

// post sends in as JSON and decodes a successful JSON response into out.
func (c *Client) post(ctx context.Context, endpoint string, in, out any) error {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, bodyLimit))
	_, _ = io.Copy(io.Discard, resp.Body)

	switch {
	case resp.StatusCode == http.StatusNotFound:
		return ErrNotFound
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return &StatusError{Code: resp.StatusCode, Body: strings.TrimSpace(string(data))}
	}
	if out == nil || len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	return json.Unmarshal(data, out)
}
