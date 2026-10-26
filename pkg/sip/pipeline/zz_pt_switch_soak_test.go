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

package pipeline

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/livekit"
	"github.com/stretchr/testify/require"

	"github.com/livekit/sip/pkg/sip/pipeline/elements/hop"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/sipbin"
)

// TestPayloadTypeSwitchSoak replays the Cisco payload type renumbering on the
// production receive path (sipbin, hop, iosip): H264 on PT 96, re-INVITE, PT
// 98 for 70 ms then PT 126 on the same SSRC, with packet loss. It fails when
// the RTP session stops counting packets while the sender keeps sending, or
// when the pipeline does not reach NULL.
// LKSIP_PT_SWITCH_MINUTES sets the duration; the test is skipped without it.
func TestPayloadTypeSwitchSoak(t *testing.T) {
	minutes, _ := strconv.Atoi(os.Getenv("LKSIP_PT_SWITCH_MINUTES"))
	if minutes <= 0 {
		t.Skip("LKSIP_PT_SWITCH_MINUTES not set")
	}
	lossPermille, _ := strconv.Atoi(os.Getenv("LKSIP_PT_SWITCH_LOSS_PERMILLE"))
	withAudio := os.Getenv("LKSIP_PT_SWITCH_AUDIO") != ""

	loop := glib.NewMainLoop(glib.MainContextDefault(), false)
	go loop.Run()
	defer loop.Quit()

	pl, err := gst.NewPipeline("pt-switch")
	require.NoError(t, err)

	formats, err := glib.NewArray([]interface{}{
		gst.NewCapsFromString("application/x-rtp,media=audio,encoding-name=OPUS,clock-rate=48000,encoding-params=(string)2"),
		gst.NewCapsFromString("application/x-rtp,media=audio,encoding-name=G722,clock-rate=8000"),
		makeH264BaselineCaps(),
		gst.NewCapsFromString("application/x-rtp,media=video,encoding-name=H264,clock-rate=90000"),
	})
	require.NoError(t, err)
	sb, err := gst.NewElementWithProperties("sipbin", map[string]interface{}{
		"ip":         "127.0.0.1",
		"port-start": uint(30000),
		"port-end":   uint(30999),
		"formats":    formats,
	})
	require.NoError(t, err)
	io, err := gst.NewElementWithProperties("iosip", map[string]interface{}{
		"video-width":           uint(640),
		"video-height":          uint(360),
		"framerate":             uint(15),
		"screenshare-width":     uint(640),
		"screenshare-height":    uint(360),
		"screenshare-framerate": uint(5),
	})
	require.NoError(t, err)
	require.NoError(t, pl.AddMany(sb, io))

	var branches atomic.Int32
	var hopMu sync.Mutex
	hopSrcs := map[string]*gst.Element{}
	sb.Connect("pad-added", func(_ *gst.Element, pad *gst.Pad) {
		name := pad.GetName()
		var session, ssrc, pt uint
		if _, err := fmt.Sscanf(name, "recv_rtp_src_%d_%d_%d", &session, &ssrc, &pt); err != nil {
			return
		}
		sink := io.GetRequestPad(fmt.Sprintf("recv_rtp_sink_%d_%d_%d", session, ssrc, pt))
		if sink == nil {
			t.Errorf("no iosip sink pad for %s", name)
			return
		}
		hs, hsrc, err := hop.NewPair()
		if err != nil {
			t.Errorf("hop pair: %v", err)
			return
		}
		_ = pl.AddMany(hsrc, hs)
		hsrc.GetStaticPad("src").Link(sink)
		pad.Link(hs.GetStaticPad("sink"))
		hsrc.SyncStateWithParent()
		hs.SyncStateWithParent()
		branches.Add(1)
		hopMu.Lock()
		hopSrcs[fmt.Sprintf("%d_%d", session, pt)] = hsrc
		hopMu.Unlock()
		t.Logf("%s linked recv branch session=%d ssrc=%d pt=%d", time.Now().Format("15:04:05.000"), session, ssrc, pt)
	})
	io.Connect("pad-added", func(_ *gst.Element, pad *gst.Pad) {
		if !strings.HasPrefix(pad.GetName(), "send_rtp_src_") {
			return
		}
		fs, err := gst.NewElementWithProperties("fakesink", map[string]interface{}{"sync": false, "async": false})
		if err != nil {
			return
		}
		_ = pl.Add(fs)
		fs.SyncStateWithParent()
		pad.Link(fs.GetStaticPad("sink"))
	})

	audioLines := []string{"m=audio 40000 RTP/AVP 9", "a=rtpmap:9 G722/8000", "a=sendrecv"}
	offer := func(videoPTs string, rtpmaps ...string) string {
		lines := []string{"v=0", "o=tandberg 0 1 IN IP4 127.0.0.1", "s=-", "c=IN IP4 127.0.0.1", "t=0 0"}
		lines = append(lines, audioLines...)
		lines = append(lines, "m=video 40002 RTP/AVP "+videoPTs)
		lines = append(lines, rtpmaps...)
		lines = append(lines, "a=rtcp-fb:* nack pli", "a=rtcp-fb:* ccm fir", "a=content:main", "a=sendrecv")
		return strings.Join(lines, "\r\n") + "\r\n"
	}
	h264 := func(pt int) []string {
		return []string{
			fmt.Sprintf("a=rtpmap:%d H264/90000", pt),
			fmt.Sprintf("a=fmtp:%d profile-level-id=42e01f;packetization-mode=1", pt),
		}
	}
	emitOffer := func(sdp string) string {
		res, err := sb.Emit("offer-sdp", sdp)
		require.NoError(t, err)
		answer, ok := res.(string)
		require.True(t, ok)
		_, err = sb.Emit("ack-sdp", "")
		require.NoError(t, err)
		return answer
	}

	answer := emitOffer(offer("96", h264(96)...))
	m := regexp.MustCompile(`m=video (\d+) `).FindStringSubmatch(answer)
	require.Len(t, m, 2, answer)
	videoPort, _ := strconv.Atoi(m[1])
	ma := regexp.MustCompile(`m=audio (\d+) `).FindStringSubmatch(answer)
	require.Len(t, ma, 2, answer)
	audioPort, _ := strconv.Atoi(ma[1])

	var g722Valve, opusValve *gst.Element
	if withAudio {
		mux, err := gst.NewElement("funnel")
		require.NoError(t, err)
		aout, err := gst.NewElementWithProperties("udpsink", map[string]interface{}{
			"host": "127.0.0.1", "port": audioPort, "sync": false, "async": false,
		})
		require.NoError(t, err)
		require.NoError(t, pl.AddMany(mux, aout))
		require.NoError(t, mux.Link(aout))
		branch := func(enc, pay string, pt uint, rate int, drop bool) *gst.Element {
			src, err := gst.NewElementWithProperties("audiotestsrc", map[string]interface{}{"is-live": true, "wave": 8})
			require.NoError(t, err)
			caps, err := gst.NewElementWithProperties("capsfilter", map[string]interface{}{
				"caps": gst.NewCapsFromString(fmt.Sprintf("audio/x-raw,rate=%d,channels=1", rate)),
			})
			require.NoError(t, err)
			conv, err := gst.NewElement("audioconvert")
			require.NoError(t, err)
			res, err := gst.NewElement("audioresample")
			require.NoError(t, err)
			e, err := gst.NewElement(enc)
			require.NoError(t, err)
			py, err := gst.NewElementWithProperties(pay, map[string]interface{}{"pt": pt, "ssrc": uint(864102650)})
			require.NoError(t, err)
			valve, err := gst.NewElementWithProperties("valve", map[string]interface{}{"drop": drop})
			require.NoError(t, err)
			chain := []*gst.Element{src, conv, res, caps, e, py, valve}
			require.NoError(t, pl.AddMany(chain...))
			require.NoError(t, gst.ElementLinkMany(chain...))
			require.NoError(t, valve.Link(mux))
			return valve
		}
		g722Valve = branch("avenc_g722", "rtpg722pay", 9, 16000, false)
		opusValve = branch("opusenc", "rtpopuspay", 114, 48000, true)
	}

	src, err := gst.NewElementWithProperties("videotestsrc", map[string]interface{}{"is-live": true, "pattern": 18})
	require.NoError(t, err)
	caps, err := gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString("video/x-raw,format=I420,width=320,height=180,framerate=15/1"),
	})
	require.NoError(t, err)
	enc, err := gst.NewElementWithProperties("x264enc", map[string]interface{}{
		"tune": 0x4, "speed-preset": 1, "key-int-max": 8, "bframes": 0, "bitrate": 300,
	})
	require.NoError(t, err)
	pay, err := gst.NewElementWithProperties("rtph264pay", map[string]interface{}{
		"pt": uint(96), "ssrc": uint(861957632), "config-interval": -1, "mtu": 1200,
	})
	require.NoError(t, err)
	out, err := gst.NewElementWithProperties("udpsink", map[string]interface{}{
		"host": "127.0.0.1", "port": videoPort, "sync": false, "async": false,
	})
	require.NoError(t, err)
	sender := []*gst.Element{src, caps, enc, pay, out}
	require.NoError(t, pl.AddMany(sender...))
	require.NoError(t, gst.ElementLinkMany(sender...))

	var sent, dropped atomic.Uint64
	burst := 0
	out.GetStaticPad("sink").AddProbe(gst.PadProbeTypeBuffer, func(_ *gst.Pad, _ *gst.PadProbeInfo) gst.PadProbeReturn {
		sent.Add(1)
		if burst > 0 {
			burst--
			dropped.Add(1)
			return gst.PadProbeDrop
		}
		if lossPermille > 0 && rand.IntN(1000) < lossPermille {
			if rand.IntN(50) == 0 {
				burst = 10 + rand.IntN(30)
			}
			dropped.Add(1)
			return gst.PadProbeDrop
		}
		return gst.PadProbeOK
	})

	require.NoError(t, pl.SetState(gst.StatePlaying))

	cameraReceived := func() (uint64, *sipbin.RxDiag) {
		v, err := sb.Emit("stats")
		if err != nil {
			return 0, nil
		}
		st, ok := v.(*gst.Structure)
		if !ok {
			return 0, nil
		}
		sv, err := st.GetValue(livekit.TrackSource_CAMERA.String())
		if err != nil {
			return 0, nil
		}
		av, ok := sv.(glib.ArbitraryValue)
		if !ok {
			return 0, nil
		}
		stats, ok := av.Data.(*sipbin.RTPSessionStats)
		if !ok {
			return 0, nil
		}
		var n uint64
		for _, s := range stats.Sources {
			if !s.Internal {
				n += s.PacketsReceived
			}
		}
		return n, stats.Rx
	}

	time.Sleep(15 * time.Second)
	t.Logf("%s re-INVITE: PT 96 -> 98 -> 126", time.Now().Format("15:04:05.000"))
	if withAudio {
		audioLines = []string{"m=audio 40000 RTP/AVP 114 9", "a=rtpmap:114 opus/48000/2", "a=rtpmap:9 G722/8000", "a=sendrecv"}
	}
	emitOffer(offer("126 98", append(h264(126), h264(98)...)...))
	if withAudio {
		require.NoError(t, g722Valve.SetProperty("drop", true))
		require.NoError(t, opusValve.SetProperty("drop", false))
	}
	require.NoError(t, pay.SetProperty("pt", uint(98)))
	time.Sleep(70 * time.Millisecond)
	require.NoError(t, pay.SetProperty("pt", uint(126)))

	time.Sleep(10 * time.Second)
	hopMu.Lock()
	newestActive := false
	for key, hsrc := range hopSrcs {
		path, sel, active := walkDownstream(hsrc.GetStaticPad("src"), recvWalkDepth)
		t.Logf("branch %s selector=%s active=%t path=%v", key, sel, active, path)
		if key == "1_126" {
			newestActive = active
		}
	}
	hopMu.Unlock()
	if !newestActive {
		closeSoak(t, pl)
		t.Fatal("the camera selector stayed on a branch that no longer receives RTP")
	}

	deadline := time.Now().Add(time.Duration(minutes) * time.Minute)
	var lastRecv, lastSent uint64
	flat := 0
	for tick := 1; time.Now().Before(deadline); tick++ {
		time.Sleep(2 * time.Second)
		recv, rx := cameraReceived()
		s := sent.Load()
		if recv == lastRecv && s > lastSent {
			flat++
		} else {
			flat = 0
		}
		if flat == 5 {
			t.Logf("%s RTP session frozen at %d packets, sender at %d (dropped %d)", time.Now().Format("15:04:05.000"), recv, s, dropped.Load())
			if rx != nil {
				t.Logf("rx: %+v", *rx)
			}
			t.Logf("threads:\n%s", threadSummary())
			dumpSoakDot(t, pl, "pt-switch-frozen")
			closeSoak(t, pl)
			t.Fatal("the RTP session stopped counting packets")
		}
		if tick%30 == 0 {
			t.Logf("%s received=%d sent=%d dropped=%d branches=%d", time.Now().Format("15:04:05"), recv, s, dropped.Load(), branches.Load())
		}
		lastRecv, lastSent = recv, s
	}
	closeSoak(t, pl)
}

func closeSoak(t *testing.T, pl *gst.Pipeline) {
	done := make(chan error, 1)
	go func() { done <- pl.SetState(gst.StateNull) }()
	select {
	case err := <-done:
		t.Logf("pipeline reached NULL: err=%v", err)
	case <-time.After(30 * time.Second):
		t.Logf("threads while stuck in SetState(NULL):\n%s", threadSummary())
		t.Fatal("pipeline stuck while closing")
	}
}

func dumpSoakDot(t *testing.T, pl *gst.Pipeline, name string) {
	dir := os.Getenv("LKSIP_PT_SWITCH_DOT_DIR")
	if dir == "" {
		return
	}
	path := filepath.Join(dir, name+".dot")
	if err := os.WriteFile(path, []byte(pl.DebugBinToDotData(gst.DebugGraphShowAll)), 0o644); err != nil {
		t.Logf("dot: %v", err)
	}
}

func threadSummary() string {
	tasks, err := os.ReadDir("/proc/self/task")
	if err != nil {
		return err.Error()
	}
	counts := map[string]int{}
	for _, task := range tasks {
		base := filepath.Join("/proc/self/task", task.Name())
		comm, _ := os.ReadFile(filepath.Join(base, "comm"))
		wchan, _ := os.ReadFile(filepath.Join(base, "wchan"))
		stat, _ := os.ReadFile(filepath.Join(base, "stat"))
		state := "?"
		if i := strings.LastIndexByte(string(stat), ')'); i > 0 && i+2 < len(stat) {
			state = string(stat[i+2])
		}
		counts[fmt.Sprintf("%-16s %s %s", strings.TrimSpace(string(comm)), state, strings.TrimSpace(string(wchan)))]++
	}
	lines := make([]string, 0, len(counts))
	for k, n := range counts {
		lines = append(lines, fmt.Sprintf("%3d %s", n, k))
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}
