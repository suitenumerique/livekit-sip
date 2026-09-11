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

package h264rtppaybin

import (
	"encoding/binary"
	"fmt"
	"time"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
)

const rtpStatsPeriod = 5 * time.Second

type rtpFrame struct {
	timestamp uint32
	packets   int
	bytes     int
	nalCounts [32]int
}

type rtpWatch struct {
	frame        rtpFrame
	lastSeq      uint16
	haveSeq      bool
	seqGaps      int
	frames       int
	packets      int
	bytes        int
	maxPackets   int
	maxBytes     int
	sinceStats   time.Time
	lastKeyframe time.Time
}

// watchRTP logs the packets and NAL units of every keyframe leaving the
// payloader, and a summary of the sent frames every rtpStatsPeriod.
func (e *H264RtpPayBin) watchRTP(self *gst.Bin, pad *gst.Pad) {
	wself := glib.WeakRefInit(self)
	w := &rtpWatch{sinceStats: time.Now()}
	pad.AddProbe(gst.PadProbeTypeBuffer, func(_ *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		buf := info.GetBuffer()
		if buf == nil {
			return gst.PadProbeOK
		}
		data := buf.Bytes()
		seq, ts, marker, payload, ok := parseRTP(data)
		if !ok {
			return gst.PadProbeOK
		}
		if w.haveSeq && seq != w.lastSeq+1 {
			w.seqGaps++
		}
		w.lastSeq, w.haveSeq = seq, true

		if w.frame.packets > 0 && ts != w.frame.timestamp {
			w.finishFrame(wself, false)
		}
		w.frame.timestamp = ts
		w.frame.packets++
		w.frame.bytes += len(data)
		countNALs(payload, &w.frame.nalCounts)
		if marker {
			w.finishFrame(wself, true)
		}
		return gst.PadProbeOK
	})
}

func (w *rtpWatch) finishFrame(wself *glib.WeakRef, marker bool) {
	f := w.frame
	w.frame = rtpFrame{}
	w.frames++
	w.packets += f.packets
	w.bytes += f.bytes
	if f.packets > w.maxPackets {
		w.maxPackets = f.packets
	}
	if f.bytes > w.maxBytes {
		w.maxBytes = f.bytes
	}
	self := gst.ToGstBin(wself.Get())
	if self == nil {
		return
	}
	if f.nalCounts[5] > 0 {
		now := time.Now()
		var since time.Duration
		if !w.lastKeyframe.IsZero() {
			since = now.Sub(w.lastKeyframe)
		}
		w.lastKeyframe = now
		self.Log(CAT, gst.LevelInfo, fmt.Sprintf("RTP keyframe sent\nrtp_ts=%d\npackets=%d\nbytes=%d\nsps=%d\npps=%d\nidr_slices=%d\nsei=%d\nmarker=%t\nsince_ms=%d", f.timestamp, f.packets, f.bytes, f.nalCounts[7], f.nalCounts[8], f.nalCounts[5], f.nalCounts[6], marker, since.Milliseconds()))
	}
	if time.Since(w.sinceStats) >= rtpStatsPeriod {
		self.Log(CAT, gst.LevelInfo, fmt.Sprintf("RTP video sent\nframes=%d\npackets=%d\nbytes=%d\nmax_frame_packets=%d\nmax_frame_bytes=%d\nseq_gaps=%d", w.frames, w.packets, w.bytes, w.maxPackets, w.maxBytes, w.seqGaps))
		w.frames, w.packets, w.bytes, w.maxPackets, w.maxBytes, w.seqGaps = 0, 0, 0, 0, 0, 0
		w.sinceStats = time.Now()
	}
}

// parseRTP returns the sequence number, timestamp, marker bit and payload of
// an RTP packet (RFC 3550 §5.1).
func parseRTP(data []byte) (seq uint16, ts uint32, marker bool, payload []byte, ok bool) {
	if len(data) < 12 || data[0]>>6 != 2 {
		return 0, 0, false, nil, false
	}
	offset := 12 + 4*int(data[0]&0x0f)
	if data[0]&0x10 != 0 {
		if len(data) < offset+4 {
			return 0, 0, false, nil, false
		}
		offset += 4 + 4*int(binary.BigEndian.Uint16(data[offset+2:offset+4]))
	}
	if len(data) < offset {
		return 0, 0, false, nil, false
	}
	payload = data[offset:]
	if data[0]&0x20 != 0 && len(payload) > 0 {
		pad := int(payload[len(payload)-1])
		if pad <= len(payload) {
			payload = payload[:len(payload)-pad]
		}
	}
	return binary.BigEndian.Uint16(data[2:4]), binary.BigEndian.Uint32(data[4:8]), data[1]&0x80 != 0, payload, true
}

// countNALs counts the NAL unit types carried by an H.264 RTP payload:
// single NAL unit, STAP-A aggregate, or the first fragment of a FU-A
// (RFC 6184 §5.6, §5.7, §5.8).
func countNALs(payload []byte, counts *[32]int) {
	if len(payload) < 1 {
		return
	}
	switch nalType := payload[0] & 0x1f; nalType {
	case 24:
		p := payload[1:]
		for len(p) >= 3 {
			size := int(binary.BigEndian.Uint16(p[0:2]))
			counts[p[2]&0x1f]++
			if len(p) < 2+size {
				return
			}
			p = p[2+size:]
		}
	case 28:
		if len(payload) >= 2 && payload[1]&0x80 != 0 {
			counts[payload[1]&0x1f]++
		}
	default:
		counts[nalType]++
	}
}
