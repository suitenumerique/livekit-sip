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

package g722audio

import (
	"encoding/binary"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-gst/go-gst/gst"
	"github.com/stretchr/testify/require"

	"github.com/livekit/sip/pkg/sip/pipeline/elements/testutils"
)

func plcRtpPacket(pt uint8, seq int) []byte {
	pkt := make([]byte, 12+160)
	pkt[0], pkt[1] = 0x80, pt
	binary.BigEndian.PutUint16(pkt[2:4], uint16(seq))
	binary.BigEndian.PutUint32(pkt[4:8], uint32(seq)*160)
	binary.BigEndian.PutUint32(pkt[8:12], 0x4c50c001)
	x := uint32(seq)*2654435761 + 1
	for i := range pkt[12:] {
		x = x*1103515245 + 12345
		pkt[12+i] = byte(x >> 16)
	}
	return pkt
}

func decodedSamples(t *testing.T, lost func(int) bool, total int) int64 {
	t.Helper()
	pipeline, err := gst.NewPipeline("")
	require.NoError(t, err)
	bin, err := gst.NewElement("g722-audio")
	require.NoError(t, err)
	filter, err := gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString("audio/x-raw, format=(string)S16LE, channels=(int)1, rate=(int)16000"),
	})
	require.NoError(t, err)
	sink, err := gst.NewElementWithProperties("fakesink", map[string]interface{}{"sync": false, "async": false})
	require.NoError(t, err)
	require.NoError(t, pipeline.AddMany(bin, filter, sink))
	require.NoError(t, gst.ElementLinkMany(bin, filter, sink))

	bytes := &atomic.Int64{}
	sink.GetStaticPad("sink").AddProbe(gst.PadProbeTypeBuffer, func(_ *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		if buf := info.GetBuffer(); buf != nil {
			bytes.Add(buf.GetSize())
		}
		return gst.PadProbeOK
	})

	src := gst.NewPad("src", gst.PadDirectionSource)
	require.Equal(t, gst.PadLinkOK, src.Link(bin.GetStaticPad("sink")))
	require.NoError(t, pipeline.SetState(gst.StatePlaying))
	require.True(t, src.SetActive(true))
	require.True(t, src.PushEvent(gst.NewStreamStartEvent("plc")))
	require.True(t, src.PushEvent(gst.NewCapsEvent(gst.NewCapsFromString("application/x-rtp, media=(string)audio, clock-rate=(int)8000, encoding-name=(string)G722, payload=(int)9"))))
	require.True(t, src.PushEvent(gst.NewSegmentEvent(gst.NewFormattedSegment(gst.FormatTime))))

	for i := range total {
		pts := time.Duration(i) * 20 * time.Millisecond
		if lost(i) {
			st := gst.NewStructureFromString(fmt.Sprintf("GstRTPPacketLost, seqnum=(uint)%d, timestamp=(guint64)%d, duration=(guint64)%d", i, pts, 20*time.Millisecond))
			src.PushEvent(gst.NewCustomEvent(gst.EventTypeCustomDownstream, st.Transfer()))
			continue
		}
		buf := gst.NewBufferFromBytes(plcRtpPacket(9, i))
		buf.SetPresentationTimestamp(gst.ClockTime(pts))
		require.Equal(t, gst.FlowOK, src.Push(buf))
	}
	require.True(t, src.PushEvent(gst.NewEOSEvent()))

	msg := pipeline.GetPipelineBus().TimedPopFiltered(gst.ClockTime(10*time.Second), gst.MessageEOS|gst.MessageError)
	require.NotNil(t, msg, "no EOS")
	require.Equal(t, gst.MessageEOS, msg.Type(), msg.String())

	require.NoError(t, pipeline.SetState(gst.StateNull))
	require.True(t, src.SetActive(false))
	return bytes.Load() / 2
}

func TestG722Audio_ConcealsLostPackets(t *testing.T) {
	defer testutils.AssertNoLeaks(t)
	const total, perPacket = 50, 16000 / 50

	samples := decodedSamples(t, func(i int) bool { return i >= 20 && i < 25 }, total)
	require.GreaterOrEqual(t, samples, int64((total-1)*perPacket), "the 5 lost packets leave a hole in the decoded audio")
	require.LessOrEqual(t, samples, int64((total+1)*perPacket))
}
