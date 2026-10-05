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
	"encoding/binary"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
	"weak"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
)

// rtpContinuity carries the RTP sequence number and timestamp base of a track
// across a release and a new request of its send pad.
type rtpContinuity struct {
	mu          sync.Mutex // guards the fields below and the RtpFilter caps set under it
	pending     bool
	base        uint16
	tsOffset    uint32
	hasTsOffset bool
	prevTs      uint32
	prevAt      time.Time

	sent      atomic.Bool
	awaiting  atomic.Bool
	lastSeq   atomic.Uint32
	lastTs    atomic.Uint32
	lastAt    atomic.Int64
	offset    atomic.Uint32
	hasOffset atomic.Bool
	clockRate atomic.Uint32
}

type continuityResume struct {
	lastSeq     uint16
	base        uint16
	tsOffset    uint32
	hasTsOffset bool
	idle        time.Duration
}

// observeCaps records the timestamp-offset and clock-rate of the payloader output.
func (c *rtpContinuity) observeCaps(caps *gst.Caps) {
	if caps == nil || caps.GetSize() == 0 {
		return
	}
	st := caps.GetStructureAt(0)
	if v, err := st.GetUint("timestamp-offset"); err == nil {
		c.offset.Store(uint32(v))
		c.hasOffset.Store(true)
	}
	if v, err := st.GetValue("clock-rate"); err == nil {
		if rate, ok := v.(int); ok && rate > 0 {
			c.clockRate.Store(uint32(rate))
		}
	}
}

// observePacket records the last packet sent and reports whether it is the
// first one since resume.
func (c *rtpContinuity) observePacket(seq uint16, ts uint32, now time.Time) bool {
	c.lastSeq.Store(uint32(seq))
	c.lastTs.Store(ts)
	c.lastAt.Store(now.UnixNano())
	c.sent.Store(true)
	return c.awaiting.CompareAndSwap(true, false)
}

// resume marks the track as resuming and returns caps continuing the previous
// stream, or false when the track never sent. Called with mu held.
func (c *rtpContinuity) resume(caps *gst.Caps, now time.Time) (*gst.Caps, continuityResume, bool) {
	if caps == nil || !c.sent.Load() {
		return nil, continuityResume{}, false
	}
	lastSeq := uint16(c.lastSeq.Load())
	c.pending = true
	c.base = lastSeq + 1
	c.tsOffset = c.offset.Load()
	c.hasTsOffset = c.hasOffset.Load()
	c.prevTs = c.lastTs.Load()
	c.prevAt = time.Unix(0, c.lastAt.Load())
	c.awaiting.Store(true)
	return withRtpOffsets(caps, c.base, c.tsOffset, c.hasTsOffset), continuityResume{
		lastSeq:     lastSeq,
		base:        c.base,
		tsOffset:    c.tsOffset,
		hasTsOffset: c.hasTsOffset,
		idle:        now.Sub(c.prevAt),
	}, true
}

// filterCaps returns caps with the resume offsets while a resume is pending.
// Called with mu held.
func (c *rtpContinuity) filterCaps(caps *gst.Caps) *gst.Caps {
	if !c.pending || caps == nil {
		return caps
	}
	return withRtpOffsets(caps, c.base, c.tsOffset, c.hasTsOffset)
}

// expectedTs returns the timestamp following the previous stream after
// elapsed time at the payloader clock rate. Called with mu held.
func (c *rtpContinuity) expectedTs(at time.Time) (uint32, bool) {
	rate := c.clockRate.Load()
	if rate == 0 || c.prevAt.IsZero() {
		return 0, false
	}
	ticks := at.Sub(c.prevAt).Nanoseconds() * int64(rate) / int64(time.Second)
	return c.prevTs + uint32(ticks), true
}

// withRtpOffsets copies caps with uint seqnum-offset and timestamp-offset fields.
func withRtpOffsets(caps *gst.Caps, base uint16, tsOffset uint32, hasTsOffset bool) *gst.Caps {
	out := caps.Copy()
	for i := 0; i < out.GetSize(); i++ {
		st := out.GetStructureAt(i)
		_ = st.SetValue("seqnum-offset", uint(base))
		if hasTsOffset {
			_ = st.SetValue("timestamp-offset", uint(tsOffset))
		}
	}
	return out
}

func rtpSeqTs(buf *gst.Buffer) (uint16, uint32, bool) {
	if buf == nil || buf.GetSize() < 8 {
		return 0, 0, false
	}
	hdr := buf.Extract(0, 8)
	if len(hdr) < 8 || hdr[0]>>6 != 2 {
		return 0, 0, false
	}
	return binary.BigEndian.Uint16(hdr[2:4]), binary.BigEndian.Uint32(hdr[4:8]), true
}

// watchContinuity observes the RTP leaving the RtpFilter.
func (t *SipTrack) watchContinuity(self *gst.Bin) {
	c := t.continuity
	pad := t.RtpFilter.GetStaticPad("src")
	if c == nil || pad == nil {
		return
	}
	wself := glib.WeakRefInit(self)
	wtrack := weak.Make(t)
	pad.AddProbe(gst.PadProbeTypeBuffer|gst.PadProbeTypeBufferList|gst.PadProbeTypeEventDownstream, func(_ *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		if ev := info.GetEvent(); ev != nil {
			if ev.Type() == gst.EventTypeCaps {
				c.observeCaps(ev.ParseCaps())
			}
			return gst.PadProbeOK
		}

		first := info.GetBuffer()
		last := first
		if first == nil {
			list := info.GetBufferList()
			if list == nil || list.Length() == 0 {
				return gst.PadProbeOK
			}
			first = list.GetBufferAt(0)
			last = list.GetBufferAt(list.Length() - 1)
		}
		lastSeq, lastTs, ok := rtpSeqTs(last)
		if !ok {
			return gst.PadProbeOK
		}
		now := time.Now()
		if !c.observePacket(lastSeq, lastTs, now) {
			return gst.PadProbeOK
		}

		firstSeq, firstTs, _ := rtpSeqTs(first)
		go func() {
			t := wtrack.Value()
			self := gst.ToGstBin(wself.Get())
			if t == nil || self == nil || self.Instance() == nil {
				return
			}
			t.settleContinuity(self, firstSeq, firstTs, now)
		}()
		return gst.PadProbeOK
	})
}

// resumeContinuity sets the RtpFilter caps to continue the previous stream of
// the track on a new send pad request.
func (t *SipTrack) resumeContinuity(self *gst.Bin) {
	c := t.continuity
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	caps, r, ok := c.resume(t.Caps, time.Now())
	if !ok {
		return
	}
	if err := t.RtpFilter.SetProperty("caps", caps); err != nil {
		c.pending = false
		c.awaiting.Store(false)
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set RTP continuity caps\nkind=%d\nerr=%v", t.Kind, err))
		return
	}
	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Resuming RTP stream on send pad request\nkind=%d\nlast_seq=%d\nseqnum_offset=%d\ntimestamp_offset=%d\ntimestamp_offset_set=%t\nidle_ms=%d", t.Kind, r.lastSeq, r.base, r.tsOffset, r.hasTsOffset, r.idle.Milliseconds()))
}

// settleContinuity restores the negotiation caps on the RtpFilter once the
// first packet of the resumed stream has left it.
func (t *SipTrack) settleContinuity(self *gst.Bin, firstSeq uint16, firstTs uint32, at time.Time) {
	c := t.continuity
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.pending {
		return
	}
	c.pending = false
	if err := t.RtpFilter.SetProperty("caps", t.Caps); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to restore RTP filter caps after resume\nkind=%d\nerr=%v", t.Kind, err))
	}

	tsDriftMs := int64(0)
	if want, ok := c.expectedTs(at); ok {
		if rate := c.clockRate.Load(); rate > 0 {
			tsDriftMs = int64(int32(firstTs-want)) * 1000 / int64(rate)
		}
	}
	level := gst.LevelInfo
	if firstSeq != c.base {
		level = gst.LevelWarning
	}
	self.Log(CAT, level, fmt.Sprintf("Resumed RTP stream\nkind=%d\nfirst_seq=%d\nexpected_seq=%d\nfirst_ts=%d\nts_drift_ms=%d", t.Kind, firstSeq, c.base, firstTs, tsDriftMs))
}
