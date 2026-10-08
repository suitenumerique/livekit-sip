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

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/livekit"
)

const ssrcClearIdleTimeout = 500 * time.Millisecond

type ssrcKey struct {
	session uint
	ssrc    uint32
}

type ssrcGuard struct {
	active  atomic.Int32
	mu      sync.Mutex
	held    map[ssrcKey]int
	dropped map[ssrcKey]int
}

func (g *ssrcGuard) hold(session uint, ssrc uint32) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.held == nil {
		g.held = make(map[ssrcKey]int)
		g.dropped = make(map[ssrcKey]int)
	}
	g.held[ssrcKey{session, ssrc}]++
	g.active.Add(1)
}

func (g *ssrcGuard) release(session uint, ssrc uint32) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	key := ssrcKey{session, ssrc}
	dropped := g.dropped[key]
	if g.held[key]--; g.held[key] <= 0 {
		delete(g.held, key)
		delete(g.dropped, key)
	}
	g.active.Add(-1)
	return dropped
}

func (g *ssrcGuard) drop(session uint, ssrc uint32) bool {
	if g.active.Load() == 0 {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	key := ssrcKey{session, ssrc}
	if g.held[key] == 0 {
		return false
	}
	g.dropped[key]++
	return true
}

func (g *ssrcGuard) watch(pad *gst.Pad, session uint) {
	pad.AddProbe(gst.PadProbeTypeBuffer, func(_ *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		if g.active.Load() == 0 {
			return gst.PadProbeOK
		}
		ssrc, ok := packetSSRC(info.GetBuffer())
		if ok && g.drop(session, ssrc) {
			return gst.PadProbeDrop
		}
		return gst.PadProbeOK
	})
}

func packetSSRC(buf *gst.Buffer) (uint32, bool) {
	if buf == nil || buf.GetSize() < 8 {
		return 0, false
	}
	size := int64(12)
	if buf.GetSize() < size {
		size = 8
	}
	hdr := buf.Extract(0, size)
	if hdr[0]>>6 != 2 {
		return 0, false
	}
	if pt := hdr[1]; pt >= 192 && pt <= 223 {
		return binary.BigEndian.Uint32(hdr[4:8]), true
	}
	if len(hdr) < 12 {
		return 0, false
	}
	return binary.BigEndian.Uint32(hdr[8:12]), true
}

func waitPadIdle(pad *gst.Pad, timeout time.Duration) bool {
	done := make(chan struct{})
	var once sync.Once
	pad.AddProbe(gst.PadProbeTypeIdle, func(_ *gst.Pad, _ *gst.PadProbeInfo) gst.PadProbeReturn {
		once.Do(func() { close(done) })
		return gst.PadProbeRemove
	})
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

func clearSSRCGuarded(rtpBin *gst.Element, guard *ssrcGuard, srcPads []*gst.Pad, session uint, ssrc uint32, keep func() bool) (bool, int, error) {
	guard.hold(session, ssrc)
	cleared, err := func() (bool, error) {
		for _, pad := range srcPads {
			if !waitPadIdle(pad, ssrcClearIdleTimeout) {
				return false, fmt.Errorf("source pad %s still busy after %v", pad.GetName(), ssrcClearIdleTimeout)
			}
		}
		if keep != nil && keep() {
			return false, nil
		}
		if _, err := rtpBin.Emit("clear-ssrc", session, uint(ssrc)); err != nil {
			return false, err
		}
		return true, nil
	}()
	return cleared, guard.release(session, ssrc), err
}

func (e *SipBin) recvSrcPads(session uint) []*gst.Pad {
	e.mu.Lock()
	var srcs []*gst.Element
	if session < uint(len(e.Tracks)) {
		if track := e.Tracks[session]; track != nil {
			srcs = []*gst.Element{track.RtpSrc, track.RtcpSrc}
		}
	}
	e.mu.Unlock()

	pads := make([]*gst.Pad, 0, len(srcs))
	for _, src := range srcs {
		if src == nil {
			continue
		}
		if pad := src.GetStaticPad("src"); pad != nil {
			pads = append(pads, pad)
		}
	}
	return pads
}

func (e *SipBin) clearSSRC(self *gst.Bin, session uint, ssrc uint32, keep func() bool) {
	cleared, dropped, err := clearSSRCGuarded(e.RtpBin, e.ssrcGuard, e.recvSrcPads(session), session, ssrc, keep)
	if err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Skipped clearing SSRC from RTP session\nsource=%s\nssrc=%d\nerr=%v", livekit.TrackSource(session), ssrc, err))
		return
	}
	if dropped > 0 {
		self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Dropped packets of an SSRC while clearing it\nsource=%s\nssrc=%d\ncleared=%t\ndropped=%d", livekit.TrackSource(session), ssrc, cleared, dropped))
	}
}

func (e *SipBin) clearSSRCLater(self *gst.Bin, session uint, ssrc uint32) {
	wself := glib.WeakRefInit(self)
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		self := gst.ToGstBin(wself.Get())
		if self == nil || self.Instance() == nil {
			return
		}
		e.clearSSRC(self, session, ssrc, nil)
	}()
}
