// Copyright 2023 LiveKit, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// 	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package sip

import (
	"embed"
	"fmt"

	msdk "github.com/livekit/media-sdk"

	"github.com/livekit/sip/pkg/config"
	"github.com/livekit/sip/pkg/sip/lobby"
	"github.com/livekit/sip/res"
)

type mediaRes struct {
	enterPin []msdk.PCM16Sample
	roomJoin []msdk.PCM16Sample
	wrongPin []msdk.PCM16Sample

	enterPinFd int
	roomJoinFd int
	wrongPinFd int
	timeoutFd  int

	waitingFd  int
	noAdminFd  int
	acceptedFd int
	deniedFd   int
	noAnswerFd int
}

func (s *Server) initMediaRes(conf *config.Config) {
	s.res.enterPin = res.ReadOggAudioFile(res.EnterPinOgg)
	s.res.roomJoin = res.ReadOggAudioFile(res.RoomJoinOgg)
	s.res.wrongPin = res.ReadOggAudioFile(res.WrongPinOgg)

	lang := conf.Lang

	medias := []struct {
		name  string
		fs    embed.FS
		dstFD *int
	}{
		{"enter_pin", res.EnterPin, &s.res.enterPinFd},
		{"room_join", res.RoomJoin, &s.res.roomJoinFd},
		{"wrong_pin", res.WrongPin, &s.res.wrongPinFd},
		{"timeout", res.Lang, &s.res.timeoutFd},
		{"waiting", res.Lang, &s.res.waitingFd},
		{"noadmin", res.Lang, &s.res.noAdminFd},
		{"accepted", res.Lang, &s.res.acceptedFd},
		{"denied", res.Lang, &s.res.deniedFd},
		{"no_answer", res.Lang, &s.res.noAnswerFd},
	}
	for _, m := range medias {
		data, err := m.fs.ReadFile(fmt.Sprintf("lang/%s/%s.flac", lang, m.name))
		if err != nil {
			data, err = m.fs.ReadFile(fmt.Sprintf("lang/en/%s.flac", m.name))
		}
		if err != nil && m.name == "timeout" {
			data, err = res.WrongPin.ReadFile(fmt.Sprintf("lang/%s/wrong_pin.flac", lang))
			if err != nil {
				data, err = res.WrongPin.ReadFile("lang/en/wrong_pin.flac")
			}
		}
		if err != nil {
			panic(fmt.Errorf("failed to read embedded %s audio file: %w", m.name, err))
		}
		*m.dstFD, err = res.MemfdFromBytes(m.name, data)
		if err != nil {
			panic(fmt.Errorf("failed to memfd %s audio file: %w", m.name, err))
		}
	}
}

// lobbyFd returns the audio file of a lobby prompt.
func (r *mediaRes) lobbyFd(p lobby.Prompt) int {
	switch p {
	case lobby.PromptNoAdmin:
		return r.noAdminFd
	case lobby.PromptAccepted:
		return r.acceptedFd
	case lobby.PromptDenied:
		return r.deniedFd
	case lobby.PromptNoAnswer:
		return r.noAnswerFd
	}
	return r.waitingFd
}
