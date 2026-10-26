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
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-gst/go-gst/gst"
	"github.com/stretchr/testify/require"
)

const procNetUDP = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode ref pointer drops
  412: 0100007F:5EF4 00000000:0000 07 00000000:00000000 00:00000000 00000000     0        0 81234 2 0000000000000000 0
  413: 375B0A0A:5EF5 00000000:0000 07 00000000:0000A2C0 00:00000000 00000000     0        0 81235 2 0000000000000000 17
`

func TestParseUDPQueue(t *testing.T) {
	queued, drops, ok := parseUDPQueue(strings.NewReader(procNetUDP), 81235)
	require.True(t, ok)
	require.EqualValues(t, 0xA2C0, queued)
	require.EqualValues(t, 17, drops)

	queued, drops, ok = parseUDPQueue(strings.NewReader(procNetUDP), 81234)
	require.True(t, ok)
	require.Zero(t, queued)
	require.Zero(t, drops)

	_, _, ok = parseUDPQueue(strings.NewReader(procNetUDP), 99)
	require.False(t, ok)
}

func TestUDPSocketQueue(t *testing.T) {
	if _, err := os.Stat("/proc/net/udp"); err != nil {
		t.Skip("no /proc/net/udp")
	}
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	defer conn.Close()

	queued, drops := udpSocketQueue(conn)
	require.Zero(t, queued)
	require.Zero(t, drops)

	out, err := net.DialUDP("udp4", nil, conn.LocalAddr().(*net.UDPAddr))
	require.NoError(t, err)
	defer out.Close()
	for range 3 {
		_, err := out.Write(make([]byte, 200))
		require.NoError(t, err)
	}
	require.Eventually(t, func() bool {
		queued, _ = udpSocketQueue(conn)
		return queued > 0
	}, time.Second, 10*time.Millisecond, "unread datagrams stay in the receive queue")

	queued, drops = udpSocketQueue(nil)
	require.EqualValues(t, -1, queued)
	require.EqualValues(t, -1, drops)
}

func TestRxCounter(t *testing.T) {
	var c rxCounter
	now := time.Now()

	d := c.snapshot(now)
	require.Zero(t, d.InPackets)
	require.Equal(t, -1, d.LastPT)
	require.EqualValues(t, -1, d.LastInAgoMs)

	c.observe(96, 861957632, now)
	c.observe(126, 861957632, now)
	c.observe(126, 861957632, now.Add(time.Second))
	for i := range rxMaxSSRCs + 4 {
		c.observe(126, uint32(1000+i), now.Add(time.Second))
	}

	d = c.snapshot(now.Add(3 * time.Second))
	require.EqualValues(t, 3+rxMaxSSRCs+4, d.InPackets)
	require.Equal(t, map[uint8]uint64{96: 1, 126: 2 + rxMaxSSRCs + 4}, d.InByPT)
	require.Len(t, d.InBySSRC, rxMaxSSRCs)
	require.EqualValues(t, 3, d.InBySSRC[861957632])
	require.Equal(t, 126, d.LastPT)
	require.EqualValues(t, 2000, d.LastInAgoMs)
}

func TestRtpPayloadTypeSSRC(t *testing.T) {
	pkt := []byte{0x80, 0xfe, 0x00, 0x01, 0, 0, 0, 1, 0x33, 0x60, 0x44, 0x00, 0xaa}
	pt, ssrc, ok := rtpPayloadTypeSSRC(gst.NewBufferFromBytes(pkt))
	require.True(t, ok)
	require.EqualValues(t, 126, pt)
	require.EqualValues(t, 0x33604400, ssrc)

	_, _, ok = rtpPayloadTypeSSRC(gst.NewBufferFromBytes(pkt[:8]))
	require.False(t, ok)
	_, _, ok = rtpPayloadTypeSSRC(gst.NewBufferFromBytes(append([]byte{0x40}, pkt[1:]...)))
	require.False(t, ok)
}
