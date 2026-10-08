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
	"context"
	"encoding/binary"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/stretchr/testify/require"

	"github.com/livekit/sip/pkg/sip/pipeline/elements/testutils"
)

const clearTestSSRC = 0x2b91c859

func rtpPacket(seq uint16, ssrc uint32) []byte {
	pkt := make([]byte, 12+160)
	pkt[0], pkt[1] = 0x80, 0x00
	binary.BigEndian.PutUint16(pkt[2:4], seq)
	binary.BigEndian.PutUint32(pkt[4:8], uint32(seq)*160)
	binary.BigEndian.PutUint32(pkt[8:12], ssrc)
	for i := range pkt[12:] {
		pkt[12+i] = 0xff
	}
	return pkt
}

func rtcpSenderReport(ssrc uint32, count uint32) []byte {
	pkt := make([]byte, 28)
	pkt[0], pkt[1] = 0x80, 200
	binary.BigEndian.PutUint16(pkt[2:4], 6)
	binary.BigEndian.PutUint32(pkt[4:8], ssrc)
	binary.BigEndian.PutUint32(pkt[20:24], count)
	binary.BigEndian.PutUint32(pkt[24:28], count*160)
	return pkt
}

func TestPacketSSRC(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		ssrc uint32
		ok   bool
	}{
		{"rtp", rtpPacket(7, 0x11223344), 0x11223344, true},
		{"rtp with marker", func() []byte { p := rtpPacket(7, 0x11223344); p[1] = 0x80 | 96; return p }(), 0x11223344, true},
		{"rtcp sender report", rtcpSenderReport(0x55667788, 1), 0x55667788, true},
		{"rtcp receiver report", []byte{0x80, 201, 0, 1, 0x01, 0x02, 0x03, 0x04}, 0x01020304, true},
		{"short rtp", rtpPacket(7, 1)[:10], 0, false},
		{"too short", []byte{0x80, 0, 0, 1}, 0, false},
		{"not version 2", func() []byte { p := rtpPacket(7, 1); p[0] = 0x40; return p }(), 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ssrc, ok := packetSSRC(gst.NewBufferFromBytes(c.data))
			require.Equal(t, c.ok, ok)
			if c.ok {
				require.Equal(t, c.ssrc, ssrc)
			}
		})
	}
}

func TestSsrcGuard(t *testing.T) {
	var g ssrcGuard
	require.False(t, g.drop(3, 42))

	g.hold(3, 42)
	g.hold(3, 42)
	require.True(t, g.drop(3, 42))
	require.False(t, g.drop(3, 43))
	require.False(t, g.drop(2, 42))

	require.Equal(t, 1, g.release(3, 42))
	require.True(t, g.drop(3, 42), "still held once")
	require.Equal(t, 2, g.release(3, 42))
	require.False(t, g.drop(3, 42))
	require.Zero(t, g.active.Load())
}

type clearRig struct {
	pipeline *gst.Pipeline
	rtpBin   *gst.Element
	srcPads  []*gst.Pad
	guard    *ssrcGuard
	handlers []glib.SignalHandle

	padAdded    chan struct{}
	inClear     *atomic.Bool
	duringClear *atomic.Int32

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func newClearRig(t *testing.T) *clearRig {
	t.Helper()
	padAdded := make(chan struct{}, 16)
	inClear := &atomic.Bool{}
	duringClear := &atomic.Int32{}
	guard := &ssrcGuard{}
	r := &clearRig{guard: guard, padAdded: padAdded, inClear: inClear, duringClear: duringClear}

	pipeline, err := gst.NewPipeline("")
	require.NoError(t, err)
	r.pipeline = pipeline
	rtpBin, err := gst.NewElementWithProperties("rtpbin", map[string]interface{}{
		"latency":    uint(20),
		"autoremove": false,
	})
	require.NoError(t, err)
	r.rtpBin = rtpBin
	sink, err := gst.NewElementWithProperties("fakesink", map[string]interface{}{"sync": false, "async": false})
	require.NoError(t, err)
	require.NoError(t, pipeline.AddMany(rtpBin, sink))
	sinkPad := sink.GetStaticPad("sink")

	h, err := rtpBin.Connect("pad-added", func(_ *gst.Element, pad *gst.Pad) {
		if !strings.HasPrefix(pad.GetName(), "recv_rtp_src_0_") || sinkPad.IsLinked() {
			return
		}
		pad.Link(sinkPad)
		select {
		case padAdded <- struct{}{}:
		default:
		}
	})
	require.NoError(t, err)
	r.handlers = append(r.handlers, h)
	h, err = rtpBin.Connect("pad-removed", func(_ *gst.Element, pad *gst.Pad) {
		if strings.HasPrefix(pad.GetName(), "recv_rtp_src_0_") {
			time.Sleep(50 * time.Millisecond)
		}
	})
	require.NoError(t, err)
	r.handlers = append(r.handlers, h)

	var conns []*net.UDPConn
	for i, name := range []string{"recv_rtp_sink_0", "recv_rtcp_sink_0"} {
		conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		require.NoError(t, err)
		t.Cleanup(func() { conn.Close() })
		conns = append(conns, conn)
		socket, err := GSocketFromUDPConn(conn)
		require.NoError(t, err)
		caps := "application/x-rtp,media=audio,encoding-name=PCMU,clock-rate=8000,payload=0"
		if i == 1 {
			caps = "application/x-rtcp"
		}
		udpsrc, err := gst.NewElementWithProperties("udpsrc", map[string]interface{}{
			"socket":       socket,
			"close-socket": false,
			"caps":         gst.NewCapsFromString(caps),
		})
		require.NoError(t, err)
		require.NoError(t, pipeline.Add(udpsrc))
		srcPad := udpsrc.GetStaticPad("src")
		recvPad := rtpBin.GetRequestPad(name)
		require.NotNil(t, recvPad)
		require.Equal(t, gst.PadLinkOK, srcPad.Link(recvPad))
		guard.watch(srcPad, 0)
		r.srcPads = append(r.srcPads, srcPad)

		recvPad.AddProbe(gst.PadProbeTypeBuffer, func(_ *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
			if !inClear.Load() {
				return gst.PadProbeOK
			}
			if ssrc, ok := packetSSRC(info.GetBuffer()); ok && ssrc == clearTestSSRC {
				duringClear.Add(1)
				return gst.PadProbeDrop
			}
			return gst.PadProbeOK
		})
	}
	require.NoError(t, pipeline.SetState(gst.StatePlaying))

	rtpOut, err := net.DialUDP("udp4", nil, conns[0].LocalAddr().(*net.UDPAddr))
	require.NoError(t, err)
	t.Cleanup(func() { rtpOut.Close() })
	rtcpOut, err := net.DialUDP("udp4", nil, conns[1].LocalAddr().(*net.UDPAddr))
	require.NoError(t, err)
	t.Cleanup(func() { rtcpOut.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		ticker := time.NewTicker(2 * time.Millisecond)
		defer ticker.Stop()
		for i := 0; ; i++ {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			_, _ = rtpOut.Write(rtpPacket(uint16(i), clearTestSSRC))
			if i%5 == 0 {
				_, _ = rtcpOut.Write(rtcpSenderReport(clearTestSSRC, uint32(i)))
			}
		}
	}()
	return r
}

func (r *clearRig) close(t *testing.T) {
	r.cancel()
	r.wg.Wait()
	for _, h := range r.handlers {
		r.rtpBin.HandlerDisconnect(h)
	}
	require.NoError(t, r.pipeline.SetState(gst.StateNull))
	r.pipeline, r.rtpBin, r.srcPads = nil, nil, nil
}

func (r *clearRig) waitStream(t *testing.T) {
	t.Helper()
	select {
	case <-r.padAdded:
	case <-time.After(3 * time.Second):
		t.Fatal("no receive branch for the stream")
	}
}

func TestClearSSRC_PacketsReachRtpBinDuringClear(t *testing.T) {
	defer testutils.AssertNoLeaks(t)
	r := newClearRig(t)
	defer r.close(t)

	r.waitStream(t)
	r.inClear.Store(true)
	time.Sleep(20 * time.Millisecond)
	r.duringClear.Store(0)
	_, err := r.rtpBin.Emit("clear-ssrc", uint(0), uint(clearTestSSRC))
	r.inClear.Store(false)
	require.NoError(t, err)

	require.Positive(t, r.duringClear.Load(), "packets of the cleared SSRC reach rtpbin while clear-ssrc runs")
}

func TestClearSSRC_GuardedClearKeepsPacketsOut(t *testing.T) {
	defer testutils.AssertNoLeaks(t)
	r := newClearRig(t)
	defer r.close(t)

	for i := range 5 {
		r.waitStream(t)
		cleared, dropped, err := clearSSRCGuarded(r.rtpBin, r.guard, r.srcPads, 0, clearTestSSRC, func() bool {
			r.inClear.Store(true)
			return false
		})
		r.inClear.Store(false)
		require.NoError(t, err)
		require.True(t, cleared)
		require.Zero(t, r.duringClear.Load(), "clear %d: a packet of the SSRC reached rtpbin during clear-ssrc", i)
		require.Positive(t, dropped, "clear %d: the stream kept flowing during the clear", i)
	}
	r.waitStream(t)
}

func TestClearSSRC_KeepCancelsClear(t *testing.T) {
	defer testutils.AssertNoLeaks(t)
	r := newClearRig(t)
	defer r.close(t)

	r.waitStream(t)
	cleared, _, err := clearSSRCGuarded(r.rtpBin, r.guard, r.srcPads, 0, clearTestSSRC, func() bool { return true })
	require.NoError(t, err)
	require.False(t, cleared)
	require.Zero(t, r.guard.active.Load())
}
