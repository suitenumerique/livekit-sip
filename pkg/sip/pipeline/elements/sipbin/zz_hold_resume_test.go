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

package sipbin

import (
	"sync"
	"testing"

	"github.com/go-gst/go-gst/gst"
)

func directionOf(t *testing.T, sdp string, idx int) string {
	t.Helper()
	media := parseAnswer(t, sdp).Media(idx)
	for _, dir := range []string{"inactive", "sendonly", "recvonly"} {
		if media.HasAttribute(dir) {
			return dir
		}
	}
	return "sendrecv"
}

func lateOfferAnswer(direction string) string {
	return makeSDP("192.168.1.1",
		"m=audio 6000 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000\r\na="+direction,
		"m=video 6002 RTP/AVP 120\r\na=rtpmap:120 H264/90000\r\na=content:main\r\na="+direction,
		"m=video 6004 RTP/AVP 121\r\na=rtpmap:121 H264/90000\r\na=content:slides\r\na="+direction,
		"m=application 6006 UDP/BFCP *\r\na=floorctrl:c-s\r\na=confid:1\r\na=userid:2\r\na=bfcpver:1",
	)
}

func TestReInvite_HoldThenLateOfferResumesMedia(t *testing.T) {
	f := newFixture(t, []*gst.Caps{pcmuCaps(), h264Caps()})
	defer f.close()

	var mu sync.Mutex
	var camera, microphone bool
	if _, err := f.sipbin.Connect("available-media", func(_ *gst.Element, cam, mic, _, _ bool) {
		mu.Lock()
		camera, microphone = cam, mic
		mu.Unlock()
	}); err != nil {
		t.Fatalf("failed to connect available-media: %v", err)
	}
	available := func() (bool, bool) {
		mu.Lock()
		defer mu.Unlock()
		return camera, microphone
	}

	offer := f.emitOffer(t, "")
	if n := parseAnswer(t, offer).MediasLen(); n != 4 {
		t.Fatalf("expected 4 medias in late offer, got %d", n)
	}
	f.emitAckWithSDP(t, lateOfferAnswer("sendrecv"))
	if cam, mic := available(); !cam || !mic {
		t.Fatalf("after the first answer: camera=%t microphone=%t, want both available", cam, mic)
	}

	answer := f.emitOffer(t, lateOfferAnswer("inactive"))
	for i := range 3 {
		if dir := directionOf(t, answer, i); dir != "inactive" {
			t.Errorf("hold answer media %d: direction %s, want inactive", i, dir)
		}
	}
	f.emitAck(t)
	if cam, mic := available(); cam || mic {
		t.Fatalf("on hold: camera=%t microphone=%t, want none available", cam, mic)
	}

	resume := f.emitOffer(t, "")
	resumeMsg := parseAnswer(t, resume)
	if n := resumeMsg.MediasLen(); n != 4 {
		t.Fatalf("expected 4 medias in the resume offer, got %d", n)
	}
	for i := range 3 {
		if resumeMsg.Media(i).GetPort() == 0 {
			t.Errorf("resume offer media %d is disabled", i)
		}
		if dir := directionOf(t, resume, i); dir != "sendrecv" {
			t.Errorf("resume offer media %d: direction %s, want sendrecv", i, dir)
		}
	}
	f.emitAckWithSDP(t, lateOfferAnswer("sendrecv"))
	if cam, mic := available(); !cam || !mic {
		t.Fatalf("after resume: camera=%t microphone=%t, want both available", cam, mic)
	}
}

func TestReInvite_DeviceSendOnlyAnsweredRecvOnly(t *testing.T) {
	f := newFixture(t, []*gst.Caps{pcmuCaps(), h264Caps()})
	defer f.close()

	offer := makeSDP("192.168.1.1",
		"m=audio 5000 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000\r\na=sendonly",
		"m=video 5002 RTP/AVP 120\r\na=rtpmap:120 H264/90000\r\na=content:main\r\na=recvonly",
	)
	answer := f.emitOffer(t, offer)
	if dir := directionOf(t, answer, 0); dir != "recvonly" {
		t.Errorf("answer to a sendonly audio: direction %s, want recvonly", dir)
	}
	if dir := directionOf(t, answer, 1); dir != "sendonly" {
		t.Errorf("answer to a recvonly video: direction %s, want sendonly", dir)
	}
	f.emitAck(t)
}
