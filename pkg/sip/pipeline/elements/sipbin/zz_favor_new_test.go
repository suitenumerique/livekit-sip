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
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-gst/go-gst/gst"
	"github.com/stretchr/testify/require"

	"github.com/livekit/sip/pkg/sip/pipeline/elements/testutils"
)

const (
	addressSwitchFirstSeq = 1000
	addressSwitchSSRC     = 0x11223344
)

// runAddressSwitch feeds one PCMU stream to rtpbin session 0 through a udpsrc:
// the first packets are sent from one local port and the rest from another,
// with the same SSRC and continuous sequence numbers. It returns how many
// packets of each part left the session.
func runAddressSwitch(t *testing.T, favorNew bool, beforeSwitch, afterSwitch int) (int, int) {
	t.Helper()

	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	defer conn.Close()
	socket, err := GSocketFromUDPConn(conn)
	require.NoError(t, err)

	pipeline, err := gst.NewPipeline("")
	require.NoError(t, err)
	udpsrc, err := gst.NewElementWithProperties("udpsrc", map[string]interface{}{
		"socket":       socket,
		"close-socket": false,
		"caps":         gst.NewCapsFromString("application/x-rtp,media=audio,encoding-name=PCMU,clock-rate=8000,payload=0"),
	})
	require.NoError(t, err)
	rtpBin, err := gst.NewElementWithProperties("rtpbin", map[string]interface{}{
		"latency": uint(50),
	})
	require.NoError(t, err)
	sink, err := gst.NewElementWithProperties("fakesink", map[string]interface{}{
		"sync":  false,
		"async": false,
	})
	require.NoError(t, err)
	require.NoError(t, pipeline.AddMany(udpsrc, rtpBin, sink))
	sinkPad := sink.GetStaticPad("sink")

	var fromFirst, fromSecond atomic.Int32
	padAdded, err := rtpBin.Connect("pad-added", func(_ *gst.Element, pad *gst.Pad) {
		if !strings.HasPrefix(pad.GetName(), "recv_rtp_src_0_") || sinkPad.IsLinked() {
			return
		}
		pad.AddProbe(gst.PadProbeTypeBuffer, func(_ *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
			buf := info.GetBuffer()
			if buf == nil {
				return gst.PadProbeOK
			}
			data := buf.Bytes()
			if len(data) < 4 {
				return gst.PadProbeOK
			}
			if binary.BigEndian.Uint16(data[2:4]) < uint16(addressSwitchFirstSeq+beforeSwitch) {
				fromFirst.Add(1)
			} else {
				fromSecond.Add(1)
			}
			return gst.PadProbeOK
		})
		pad.Link(sinkPad)
	})
	require.NoError(t, err)
	defer rtpBin.HandlerDisconnect(padAdded)

	recvPad := rtpBin.GetRequestPad("recv_rtp_sink_0")
	require.NotNil(t, recvPad)
	if favorNew {
		require.NoError(t, favorNewSourceAddress(rtpBin, 0))
	}
	require.Equal(t, gst.PadLinkOK, udpsrc.GetStaticPad("src").Link(recvPad))
	require.NoError(t, pipeline.SetState(gst.StatePlaying))

	dst := conn.LocalAddr().(*net.UDPAddr)
	first, err := net.DialUDP("udp4", nil, dst)
	require.NoError(t, err)
	defer first.Close()
	second, err := net.DialUDP("udp4", nil, dst)
	require.NoError(t, err)
	defer second.Close()

	pkt := make([]byte, 12+160)
	for i := range pkt[12:] {
		pkt[12+i] = 0xff
	}
	for i := 0; i < beforeSwitch+afterSwitch; i++ {
		seq := uint16(addressSwitchFirstSeq + i)
		pkt[0], pkt[1] = 0x80, 0x00
		binary.BigEndian.PutUint16(pkt[2:4], seq)
		binary.BigEndian.PutUint32(pkt[4:8], uint32(seq)*160)
		binary.BigEndian.PutUint32(pkt[8:12], addressSwitchSSRC)
		sender := first
		if i >= beforeSwitch {
			sender = second
		}
		_, err := sender.Write(pkt)
		require.NoError(t, err)
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)

	require.NoError(t, pipeline.SetState(gst.StateNull))
	return int(fromFirst.Load()), int(fromSecond.Load())
}

func TestRtpSession_SourceAddressChange(t *testing.T) {
	t.Run("dropped by default", func(t *testing.T) {
		defer testutils.AssertNoLeaks(t)
		first, second := runAddressSwitch(t, false, 10, 50)
		require.Positive(t, first, "packets from the first address must pass")
		require.Zero(t, second, "rtpsession drops a known SSRC arriving from a new address")
	})
	t.Run("followed with favor-new", func(t *testing.T) {
		defer testutils.AssertNoLeaks(t)
		first, second := runAddressSwitch(t, true, 10, 50)
		require.Positive(t, first, "packets from the first address must pass")
		require.GreaterOrEqual(t, second, 45, "packets from the new address must pass")
	})
}
