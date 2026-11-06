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
	"slices"
	"testing"
)

func TestLocalMediaPorts(t *testing.T) {
	answer := "v=0\r\n" +
		"o=- 1 2 IN IP4 10.0.0.92\r\n" +
		"s=livekit\r\n" +
		"c=IN IP4 10.0.0.92\r\n" +
		"t=0 0\r\n" +
		"m=audio 20112 RTP/AVP 9 101\r\n" +
		"a=rtpmap:9 G722/8000\r\n" +
		"a=rtpmap:101 telephone-event/8000\r\n" +
		"a=ptime:20\r\n" +
		"a=rtcp:20113\r\n" +
		"a=sendrecv\r\n" +
		"m=video 21040 RTP/AVP 109\r\n" +
		"a=rtpmap:109 H264/90000\r\n" +
		"a=rtcp-fb:* nack pli\r\n" +
		"a=rtcp:21041\r\n" +
		"a=content:main\r\n" +
		"m=video 0 RTP/AVP 109\r\n" +
		"m=video 20500 RTP/AVP 109\r\n" +
		"a=rtpmap:109 H264/90000\r\n" +
		"a=rtcp:20501 IN IP4 10.0.0.92\r\n" +
		"a=content:slides\r\n" +
		"m=application 5070 UDP/BFCP *\r\n" +
		"a=floorctrl:s-only\r\n"

	got, err := localMediaPorts(answer)
	if err != nil {
		t.Fatalf("localMediaPorts: %v", err)
	}
	want := []any{
		"microphone", "20112/20113",
		"camera", "21040/21041",
		"screenShare", "20500/20501",
		"bfcp", "5070",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestLocalMediaPortsRejectsInvalidSDP(t *testing.T) {
	if _, err := localMediaPorts("not an sdp"); err == nil {
		t.Fatal("expected an error for an invalid SDP")
	}
}
