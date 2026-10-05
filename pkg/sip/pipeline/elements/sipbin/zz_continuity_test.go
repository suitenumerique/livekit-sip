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
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/livekit"
	"github.com/stretchr/testify/require"

	"github.com/livekit/sip/pkg/sip/pipeline/elements/h264rtppaybin"
)

const continuityRtpCaps = "application/x-rtp, media=(string)video, encoding-name=(string)H264, clock-rate=(int)90000, payload=(int)96, packetization-mode=(string)1, profile-level-id=(string)42e01f"

var registerH264RtpPayBin sync.Once

func capsUint(t *testing.T, caps *gst.Caps, field string) (uint, bool) {
	t.Helper()
	require.NotNil(t, caps)
	require.Greater(t, caps.GetSize(), 0)
	v, err := caps.GetStructureAt(0).GetUint(field)
	return v, err == nil
}

func rtpFilterCaps(t *testing.T, filter *gst.Element) *gst.Caps {
	t.Helper()
	v, err := filter.GetProperty("caps")
	require.NoError(t, err)
	caps, ok := v.(*gst.Caps)
	require.True(t, ok)
	return caps
}

func TestRtpContinuity_NoResumeBeforeFirstPacket(t *testing.T) {
	c := &rtpContinuity{}
	c.mu.Lock()
	defer c.mu.Unlock()
	_, _, ok := c.resume(gst.NewCapsFromString(continuityRtpCaps), time.Now())
	require.False(t, ok)
	require.False(t, c.pending)
}

func TestRtpContinuity_ResumeContinuesSequence(t *testing.T) {
	c := &rtpContinuity{}
	c.observeCaps(gst.NewCapsFromString("application/x-rtp, clock-rate=(int)90000, timestamp-offset=(uint)123456, seqnum-offset=(uint)40000"))
	require.False(t, c.observePacket(65535, 9000, time.Now()))

	c.mu.Lock()
	caps, r, ok := c.resume(gst.NewCapsFromString(continuityRtpCaps), time.Now())
	c.mu.Unlock()
	require.True(t, ok)
	require.Equal(t, uint16(65535), r.lastSeq)
	require.Equal(t, uint16(0), r.base)

	seq, ok := capsUint(t, caps, "seqnum-offset")
	require.True(t, ok, "seqnum-offset must be a uint field")
	require.Equal(t, uint(0), seq)
	ts, ok := capsUint(t, caps, "timestamp-offset")
	require.True(t, ok, "timestamp-offset must be a uint field")
	require.Equal(t, uint(123456), ts)

	require.True(t, c.observePacket(0, 9900, time.Now()))
	require.False(t, c.observePacket(1, 9900, time.Now()))
}

func TestRtpContinuity_FilterCapsFollowPending(t *testing.T) {
	c := &rtpContinuity{}
	c.observePacket(10, 0, time.Now())
	base := gst.NewCapsFromString(continuityRtpCaps)

	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := capsUint(t, c.filterCaps(base), "seqnum-offset")
	require.False(t, ok)

	_, _, resumed := c.resume(base, time.Now())
	require.True(t, resumed)
	seq, ok := capsUint(t, c.filterCaps(base), "seqnum-offset")
	require.True(t, ok)
	require.Equal(t, uint(11), seq)

	c.pending = false
	_, ok = capsUint(t, c.filterCaps(base), "seqnum-offset")
	require.False(t, ok)
}

func TestSipTrack_UpdateCapsAroundResume(t *testing.T) {
	pipeline, err := gst.NewPipeline("test-" + t.Name())
	require.NoError(t, err)
	filter, err := gst.NewElement("capsfilter")
	require.NoError(t, err)

	caps1 := gst.NewCapsFromString(continuityRtpCaps)
	track := &SipTrack{Kind: livekit.TrackSource_SCREEN_SHARE, Caps: caps1, RtpFilter: filter, continuity: &rtpContinuity{}}
	require.NoError(t, filter.SetProperty("caps", caps1))
	track.continuity.observePacket(100, 0, time.Now())

	track.resumeContinuity(pipeline.Bin)
	seq, ok := capsUint(t, rtpFilterCaps(t, filter), "seqnum-offset")
	require.True(t, ok)
	require.Equal(t, uint(101), seq)

	caps2 := gst.NewCapsFromString("application/x-rtp, media=(string)video, encoding-name=(string)H264, clock-rate=(int)90000, payload=(int)109")
	require.NoError(t, track.UpdateCaps(caps2))
	got := rtpFilterCaps(t, filter)
	seq, ok = capsUint(t, got, "seqnum-offset")
	require.True(t, ok, "a re-INVITE during the resume window keeps the offsets")
	require.Equal(t, uint(101), seq)
	pt, err := got.GetStructureAt(0).GetValue("payload")
	require.NoError(t, err)
	require.Equal(t, 109, pt)

	track.settleContinuity(pipeline.Bin, 101, 0, time.Now())
	_, ok = capsUint(t, rtpFilterCaps(t, filter), "seqnum-offset")
	require.False(t, ok)

	caps3 := gst.NewCapsFromString("application/x-rtp, media=(string)video, encoding-name=(string)H264, clock-rate=(int)90000, payload=(int)110")
	require.NoError(t, track.UpdateCaps(caps3))
	_, ok = capsUint(t, rtpFilterCaps(t, filter), "seqnum-offset")
	require.False(t, ok)

	track.continuity.observePacket(200, 0, time.Now())
	track.resumeContinuity(pipeline.Bin)
	got = rtpFilterCaps(t, filter)
	seq, ok = capsUint(t, got, "seqnum-offset")
	require.True(t, ok, "a re-INVITE between two streams keeps the next resume")
	require.Equal(t, uint(201), seq)
	pt, err = got.GetStructureAt(0).GetValue("payload")
	require.NoError(t, err)
	require.Equal(t, 110, pt)
}

type continuityPacket struct {
	stream int32
	seq    uint16
	ts     uint32
	pts    time.Duration
	at     time.Time
}

func TestSipTrack_ContinuityAcrossPayloaderRestart(t *testing.T) {
	registerH264RtpPayBin.Do(func() { h264rtppaybin.Register() })

	pipeline, err := gst.NewPipeline("test-" + t.Name())
	require.NoError(t, err)

	src, err := gst.NewElementWithProperties("videotestsrc", map[string]interface{}{"is-live": true})
	require.NoError(t, err)
	raw, err := gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString("video/x-raw,width=320,height=240,framerate=15/1,format=I420"),
	})
	require.NoError(t, err)
	rtpCaps := gst.NewCapsFromString(continuityRtpCaps)
	filter, err := gst.NewElementWithProperties("capsfilter", map[string]interface{}{"caps": rtpCaps})
	require.NoError(t, err)
	sink, err := gst.NewElementWithProperties("fakesink", map[string]interface{}{"sync": false, "async": false})
	require.NoError(t, err)
	require.NoError(t, pipeline.AddMany(src, raw, filter, sink))
	require.NoError(t, src.Link(raw))
	require.NoError(t, filter.Link(sink))

	track := &SipTrack{Kind: livekit.TrackSource_SCREEN_SHARE, Caps: rtpCaps, RtpFilter: filter, continuity: &rtpContinuity{}}
	track.watchContinuity(pipeline.Bin)

	var stream atomic.Int32
	var mu sync.Mutex
	var packets []continuityPacket
	record := func(buf *gst.Buffer) {
		seq, ts, ok := rtpSeqTs(buf)
		if !ok {
			return
		}
		mu.Lock()
		packets = append(packets, continuityPacket{stream: stream.Load(), seq: seq, ts: ts, pts: time.Duration(buf.PresentationTimestamp()), at: time.Now()})
		mu.Unlock()
	}
	sink.GetStaticPad("sink").AddProbe(gst.PadProbeTypeBuffer|gst.PadProbeTypeBufferList, func(_ *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		if buf := info.GetBuffer(); buf != nil {
			record(buf)
		} else if list := info.GetBufferList(); list != nil {
			list.ForEach(func(buf *gst.Buffer, _ uint) bool {
				record(buf)
				return true
			})
		}
		return gst.PadProbeOK
	})

	newChain := func() (*gst.Element, *gst.Element) {
		enc, err := gst.NewElementWithProperties("x264enc", map[string]interface{}{
			"speed-preset": int(1),
			"tune":         int(4),
			"key-int-max":  uint(15),
			"bframes":      uint(0),
		})
		require.NoError(t, err)
		pay, err := gst.NewElementWithProperties("h264rtppaybin", map[string]interface{}{})
		require.NoError(t, err)
		require.NoError(t, pipeline.AddMany(enc, pay))
		require.NoError(t, gst.ElementLinkMany(raw, enc, pay, filter))
		return enc, pay
	}

	stream.Store(1)
	enc1, pay1 := newChain()
	require.NoError(t, pipeline.SetState(gst.StatePlaying))
	time.Sleep(1500 * time.Millisecond)

	rawSrc := raw.GetStaticPad("src")
	dropID := rawSrc.AddProbe(gst.PadProbeTypeBuffer, func(_ *gst.Pad, _ *gst.PadProbeInfo) gst.PadProbeReturn {
		return gst.PadProbeDrop
	})
	time.Sleep(200 * time.Millisecond)
	raw.Unlink(enc1)
	pay1.Unlink(filter)
	require.NoError(t, enc1.SetState(gst.StateNull))
	require.NoError(t, pay1.SetState(gst.StateNull))
	require.NoError(t, pipeline.RemoveMany(enc1, pay1))
	time.Sleep(time.Second)

	mu.Lock()
	require.NotEmpty(t, packets, "no packet from the first stream")
	last := packets[len(packets)-1]
	mu.Unlock()

	track.resumeContinuity(pipeline.Bin)
	seq, ok := capsUint(t, rtpFilterCaps(t, filter), "seqnum-offset")
	require.True(t, ok)
	require.Equal(t, uint(last.seq+1), seq)

	stream.Store(2)
	enc2, pay2 := newChain()
	require.True(t, enc2.SyncStateWithParent())
	require.True(t, pay2.SyncStateWithParent())
	rawSrc.RemoveProbe(dropID)
	time.Sleep(1500 * time.Millisecond)

	require.Eventually(t, func() bool {
		_, ok := capsUint(t, rtpFilterCaps(t, filter), "seqnum-offset")
		return !ok
	}, 2*time.Second, 50*time.Millisecond, "resume offsets still on the RTP filter")

	stream.Store(3)
	require.NoError(t, raw.SetProperty("caps", gst.NewCapsFromString("video/x-raw,width=640,height=480,framerate=15/1,format=I420")))
	time.Sleep(1500 * time.Millisecond)

	encCaps := enc2.GetStaticPad("src").GetCurrentCaps()
	require.NoError(t, pipeline.SetState(gst.StateNull))

	require.NotNil(t, encCaps)
	width, err := encCaps.GetStructureAt(0).GetValue("width")
	require.NoError(t, err)
	require.Equal(t, 640, width, "the encoder did not renegotiate")

	mu.Lock()
	defer mu.Unlock()
	var first *continuityPacket
	prev := last
	resumed := 0
	for i := range packets {
		p := packets[i]
		if p.stream < 2 {
			continue
		}
		if first == nil {
			first = &packets[i]
			require.Equal(t, last.seq+1, p.seq, "first packet of the new stream does not continue the sequence")
		} else {
			require.Equal(t, prev.seq+1, p.seq, "sequence jumped at packet %d of stream %d", resumed, p.stream)
		}
		prev = p
		resumed++
	}
	require.NotNil(t, first, "no packet from the new stream")
	require.Greater(t, resumed, 30)

	byPts := last.ts + uint32((first.pts-last.pts).Nanoseconds()*90000/int64(time.Second))
	require.InDelta(t, 0, int64(int32(first.ts-byPts)), 900, "timestamp not continuous with the running time")
	byClock := last.ts + uint32(first.at.Sub(last.at).Nanoseconds()*90000/int64(time.Second))
	require.InDelta(t, 0, int64(int32(first.ts-byClock)), 45000, "timestamp off the elapsed time by more than 0.5 s")
}
