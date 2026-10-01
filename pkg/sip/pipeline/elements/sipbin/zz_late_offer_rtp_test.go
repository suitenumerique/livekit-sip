// Copyright 2026 LiveKit, Inc.
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

package sipbin

import (
	"encoding/binary"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/stretchr/testify/require"

	"github.com/livekit/protocol/livekit"
)

// sendPCMU sends count PCMU packets of one SSRC to dst, 20 ms apart, starting
// at sequence number firstSeq.
func sendPCMU(t *testing.T, conn *net.UDPConn, firstSeq uint16, count int) {
	t.Helper()
	pkt := make([]byte, 12+160)
	for i := range pkt[12:] {
		pkt[12+i] = 0xff
	}
	for i := 0; i < count; i++ {
		seq := firstSeq + uint16(i)
		pkt[0], pkt[1] = 0x80, 0x00
		binary.BigEndian.PutUint16(pkt[2:4], seq)
		binary.BigEndian.PutUint32(pkt[4:8], uint32(seq)*160)
		binary.BigEndian.PutUint32(pkt[8:12], 0x55667788)
		_, err := conn.Write(pkt)
		require.NoError(t, err)
		time.Sleep(20 * time.Millisecond)
	}
}

// TestLateOffer_RtpBeforeAnswer sends device RTP to the offered audio port
// while the pipeline is PLAYING and the answer has not arrived yet, then
// applies the answer: the audio receive path must not fail and must deliver
// the packets that follow.
func TestLateOffer_RtpBeforeAnswer(t *testing.T) {
	f := newFixture(t, []*gst.Caps{pcmuCaps(), h264Caps()})

	offer := f.emitOffer(t, "")
	require.NotEmpty(t, offer)
	var audioPort uint
	for _, m := range parseAnswer(t, offer).Medias() {
		if m.GetMedia() == "audio" {
			audioPort = m.GetPort()
			break
		}
	}
	require.NotZero(t, audioPort, "no audio port in the late offer")

	audioSink, err := gst.NewElementWithProperties("fakesink", map[string]any{"sync": false, "async": false})
	require.NoError(t, err)
	require.NoError(t, f.pipeline.Add(audioSink))
	audioCount := addBufferProbe(t, audioSink)

	audioPrefix := fmt.Sprintf("recv_rtp_src_%d_", livekit.TrackSource_MICROPHONE)
	waudioSink := glib.WeakRefInit(audioSink)
	padAdded, err := f.sipbin.Connect("pad-added", func(_ *gst.Element, pad *gst.Pad) {
		if !strings.HasPrefix(pad.GetName(), audioPrefix) {
			return
		}
		s := gst.ToElement(waudioSink.Get())
		if s == nil {
			return
		}
		s.SyncStateWithParent()
		pad.Link(s.GetStaticPad("sink"))
	})
	require.NoError(t, err)
	defer f.sipbin.HandlerDisconnect(padAdded)

	require.NoError(t, f.pipeline.SetState(gst.StatePlaying))
	cr, st := f.pipeline.GetState(gst.StatePlaying, gst.ClockTime(5*time.Second))
	require.Equal(t, gst.StateChangeSuccess, cr)
	require.Equal(t, gst.StatePlaying, st)

	device, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(audioPort)})
	require.NoError(t, err)
	defer device.Close()

	sendPCMU(t, device, 100, 10)

	answer := makeSDP("127.0.0.1",
		"m=audio 6000 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000",
		"m=video 0 RTP/AVP 126\r\na=rtpmap:126 H264/90000\r\na=content:main",
		"m=video 0 RTP/AVP 126\r\na=rtpmap:126 H264/90000\r\na=content:slides",
		"m=application 0 UDP/BFCP *",
	)
	f.emitAckWithSDP(t, answer)

	sendPCMU(t, device, 110, 50)
	time.Sleep(500 * time.Millisecond)

	var streamErrors []string
	bus := f.pipeline.GetPipelineBus()
	for msg := bus.TimedPopFiltered(0, gst.MessageError); msg != nil; msg = bus.TimedPopFiltered(0, gst.MessageError) {
		if gerr := msg.ParseError(); gerr != nil {
			streamErrors = append(streamErrors, fmt.Sprintf("%s: %s (%s)", msg.Source(), gerr.Error(), gerr.DebugString()))
		}
	}

	f.close()

	require.Empty(t, streamErrors, "the audio receive path failed before the answer was applied")
	require.Positive(t, audioCount.Load(), "no audio buffer delivered after the answer")
}
