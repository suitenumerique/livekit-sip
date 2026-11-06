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
	"strconv"
	"strings"

	"github.com/pion/sdp/v3"
)

func localMediaPorts(data string) ([]any, error) {
	var desc sdp.SessionDescription
	if err := desc.UnmarshalString(data); err != nil {
		return nil, err
	}
	var fields []any
	for _, m := range desc.MediaDescriptions {
		if m.MediaName.Port.Value == 0 {
			continue
		}
		ports := strconv.Itoa(m.MediaName.Port.Value)
		if rtcp, ok := m.Attribute("rtcp"); ok {
			if f := strings.Fields(rtcp); len(f) > 0 {
				ports += "/" + f[0]
			}
		}
		fields = append(fields, mediaPortsKey(m), ports)
	}
	return fields, nil
}

func mediaPortsKey(m *sdp.MediaDescription) string {
	content, _ := m.Attribute("content")
	switch m.MediaName.Media {
	case "audio":
		if content == "slides" {
			return "screenShareAudio"
		}
		return "microphone"
	case "video":
		if content == "slides" {
			return "screenShare"
		}
		return "camera"
	case "application":
		return "bfcp"
	}
	return m.MediaName.Media
}

func (o *MediaOrchestrator) logLocalPorts(stage, data string) {
	fields, err := localMediaPorts(data)
	if err != nil {
		o.log.Warnw("Cannot read the local media ports", err, "stage", stage)
		return
	}
	o.log.Infow("Local media ports", append([]any{"stage", stage}, fields...)...)
}
