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
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-gst/go-gst/gst"

	"github.com/livekit/sip/pkg/sip/pipeline/threadinfo"
)

const (
	rxMaxSSRCs       = 16
	rxStaleThreshold = 5 * time.Second
)

// RxDiag describes the RTP of a track on its way into the rtpbin.
type RxDiag struct {
	InPackets    uint64
	InByPT       map[uint8]uint64
	InBySSRC     map[uint32]uint64
	LastPT       int
	LastInAgoMs  int64
	UdpsrcFlow   string
	UdpsrcTask   string
	UdpsrcThread string
	SocketQueued int64
	SocketDrops  int64
}

// rxCounter counts the RTP packets entering the rtpbin, per payload type and
// per SSRC.
type rxCounter struct {
	mu     sync.Mutex
	total  uint64
	byPT   map[uint8]uint64
	bySSRC map[uint32]uint64
	lastPT int
	lastAt time.Time
}

func (c *rxCounter) observe(pt uint8, ssrc uint32, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.byPT == nil {
		c.byPT = make(map[uint8]uint64)
		c.bySSRC = make(map[uint32]uint64)
	}
	c.total++
	c.byPT[pt]++
	if _, ok := c.bySSRC[ssrc]; ok || len(c.bySSRC) < rxMaxSSRCs {
		c.bySSRC[ssrc]++
	}
	c.lastPT = int(pt)
	c.lastAt = now
}

func (c *rxCounter) snapshot(now time.Time) RxDiag {
	c.mu.Lock()
	defer c.mu.Unlock()
	d := RxDiag{
		InPackets:    c.total,
		InByPT:       make(map[uint8]uint64, len(c.byPT)),
		InBySSRC:     make(map[uint32]uint64, len(c.bySSRC)),
		LastPT:       -1,
		LastInAgoMs:  -1,
		SocketQueued: -1,
		SocketDrops:  -1,
	}
	for pt, n := range c.byPT {
		d.InByPT[pt] = n
	}
	for ssrc, n := range c.bySSRC {
		d.InBySSRC[ssrc] = n
	}
	if !c.lastAt.IsZero() {
		d.LastPT = c.lastPT
		d.LastInAgoMs = now.Sub(c.lastAt).Milliseconds()
	}
	return d
}

func rtpPayloadTypeSSRC(buf *gst.Buffer) (uint8, uint32, bool) {
	if buf == nil || buf.GetSize() < 12 {
		return 0, 0, false
	}
	hdr := buf.Extract(0, 12)
	if len(hdr) < 12 || hdr[0]>>6 != 2 {
		return 0, 0, false
	}
	return hdr[1] & 0x7f, binary.BigEndian.Uint32(hdr[8:12]), true
}

// watchRx counts the RTP packets reaching the rtpbin through pad.
func (t *SipTrack) watchRx(pad *gst.Pad) {
	rx := t.rx
	if rx == nil || pad == nil {
		return
	}
	pad.AddProbe(gst.PadProbeTypeBuffer, func(_ *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		if pt, ssrc, ok := rtpPayloadTypeSSRC(info.GetBuffer()); ok {
			rx.observe(pt, ssrc, time.Now())
		}
		return gst.PadProbeOK
	})
}

func (t *SipTrack) rxDiag(now time.Time) *RxDiag {
	if t.rx == nil {
		return nil
	}
	d := t.rx.snapshot(now)
	if t.RtpSrc != nil {
		if pad := t.RtpSrc.GetStaticPad("src"); pad != nil {
			d.UdpsrcFlow = pad.GetLastFlowReturn().String()
			d.UdpsrcTask = taskStateName(pad.GetTaskState())
		}
		if d.LastInAgoMs < 0 || time.Duration(d.LastInAgoMs)*time.Millisecond > rxStaleThreshold {
			if snap, err := threadinfo.Take(); err == nil {
				d.UdpsrcThread = snap.Describe(threadinfo.PadTaskName(t.RtpSrc.GetName(), "src"), nil)
			}
		}
	}
	d.SocketQueued, d.SocketDrops = udpSocketQueue(t.rtpConn)
	return &d
}

func taskStateName(s gst.TaskState) string {
	switch s {
	case gst.TaskStarted:
		return "started"
	case gst.TaskPaused:
		return "paused"
	default:
		return "stopped"
	}
}

// udpSocketQueue returns the bytes waiting in the receive queue of conn and
// the datagrams the kernel dropped on it, or -1 when unknown.
func udpSocketQueue(conn *net.UDPConn) (int64, int64) {
	if conn == nil {
		return -1, -1
	}
	inode, ok := socketInode(conn)
	if !ok {
		return -1, -1
	}
	f, err := os.Open("/proc/net/udp")
	if err != nil {
		return -1, -1
	}
	defer f.Close()
	queued, drops, ok := parseUDPQueue(f, inode)
	if !ok {
		return -1, -1
	}
	return queued, drops
}

func socketInode(conn *net.UDPConn) (uint64, bool) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return 0, false
	}
	var link string
	var linkErr error
	if err := raw.Control(func(fd uintptr) {
		link, linkErr = os.Readlink(fmt.Sprintf("/proc/self/fd/%d", fd))
	}); err != nil || linkErr != nil {
		return 0, false
	}
	var inode uint64
	if _, err := fmt.Sscanf(link, "socket:[%d]", &inode); err != nil {
		return 0, false
	}
	return inode, true
}

// parseUDPQueue reads the rx_queue and drops columns of the /proc/net/udp
// line of the socket with the given inode.
func parseUDPQueue(r io.Reader, inode uint64) (int64, int64, bool) {
	want := strconv.FormatUint(inode, 10)
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 13 || f[9] != want {
			continue
		}
		queues := strings.SplitN(f[4], ":", 2)
		if len(queues) != 2 {
			return 0, 0, false
		}
		queued, err := strconv.ParseInt(queues[1], 16, 64)
		if err != nil {
			return 0, 0, false
		}
		drops, err := strconv.ParseInt(f[12], 10, 64)
		if err != nil {
			return 0, 0, false
		}
		return queued, drops, true
	}
	return 0, 0, false
}
