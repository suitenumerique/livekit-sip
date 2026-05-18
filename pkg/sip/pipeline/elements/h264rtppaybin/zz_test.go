package h264rtppaybin_test

import (
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/h264rtppaybin"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/testutils"
)

func TestMain(m *testing.M) {
	gst.Init(nil)
	h264rtppaybin.Register()
	os.Exit(m.Run())
}

// buildPipeline constructs: videotestsrc → capsfilter(raw) → x264enc →
// h264rtppaybin → capsfilter(rtp with profile-level-id) → fakesink.
func buildPipeline(t *testing.T, downPLID string, numBuffers int) (*gst.Pipeline, *gst.Element, *gst.Pad) {
	t.Helper()

	const (
		width  = 320
		height = 240
		fps    = 15
	)

	p, err := gst.NewPipeline("h264rtppaybin-test-" + downPLID)
	if err != nil {
		t.Fatal("pipeline:", err)
	}

	src, err := gst.NewElementWithProperties("videotestsrc", map[string]interface{}{
		"num-buffers": numBuffers,
		"is-live":     true,
	})
	if err != nil {
		t.Fatal("videotestsrc:", err)
	}

	rawCaps, err := gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString(
			fmt.Sprintf("video/x-raw,width=%d,height=%d,framerate=%d/1,format=I420", width, height, fps),
		),
	})
	if err != nil {
		t.Fatal("raw capsfilter:", err)
	}

	enc, err := gst.NewElementWithProperties("x264enc", map[string]interface{}{
		"speed-preset": int(1),
		"key-int-max":  uint(12),
		"bframes":      uint(0),
	})
	if err != nil {
		t.Fatal("x264enc:", err)
	}

	bin, err := gst.NewElementWithProperties("h264rtppaybin", map[string]interface{}{})
	if err != nil {
		t.Fatal("h264rtppaybin:", err)
	}

	rtpCaps, err := gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString(fmt.Sprintf(
			"application/x-rtp, media=(string)video, encoding-name=(string)H264, clock-rate=(int)90000, payload=(int)96, packetization-mode=(string)1, profile-level-id=(string)%s",
			downPLID,
		)),
	})
	if err != nil {
		t.Fatal("rtp capsfilter:", err)
	}

	sink, err := gst.NewElementWithProperties("fakesink", map[string]interface{}{
		"sync":  false,
		"async": false,
	})
	if err != nil {
		t.Fatal("fakesink:", err)
	}

	if err := p.AddMany(src, rawCaps, enc, bin, rtpCaps, sink); err != nil {
		t.Fatal("AddMany:", err)
	}
	if err := gst.ElementLinkMany(src, rawCaps, enc, bin, rtpCaps, sink); err != nil {
		t.Fatal("LinkMany:", err)
	}

	return p, bin, sink.GetStaticPad("sink")
}

// runPipeline cycles the pipeline through PLAYING, waits for EOS or
// timeout, then returns buffers received on the sink pad.
func runPipeline(t *testing.T, p *gst.Pipeline, sinkPad *gst.Pad) int32 {
	t.Helper()

	var bufferCount atomic.Int32
	sinkPad.AddProbe(gst.PadProbeTypeBuffer|gst.PadProbeTypeBufferList, func(_ *gst.Pad, _ *gst.PadProbeInfo) gst.PadProbeReturn {
		bufferCount.Add(1)
		return gst.PadProbeOK
	})

	if err := p.SetState(gst.StatePlaying); err != nil {
		t.Fatal("SetState PLAYING:", err)
	}
	defer func() {
		if err := p.SetState(gst.StateNull); err != nil {
			t.Error("SetState NULL:", err)
		}
	}()

	bus := p.GetPipelineBus()
	timeout := gst.ClockTime(time.Second)
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		msg := bus.TimedPop(timeout)
		if msg == nil {
			continue
		}
		switch msg.Type() {
		case gst.MessageEOS:
			return bufferCount.Load()
		case gst.MessageError:
			gerr := msg.ParseError()
			t.Fatal("pipeline error:", gerr.Error())
		}
	}
	t.Fatal("timed out waiting for EOS")
	return 0
}

// TestProfileNegotiation exercises the bin end-to-end against a range of
// downstream profile-level-id values, verifying that buffers flow.
func TestProfileNegotiation(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	cases := []struct {
		name string
		plid string
	}{
		{"constrained_baseline_3_1", "42e01f"},
		{"baseline_3_1", "42001f"},
		{"main_3_1", "4d001f"},
		{"high_3_1", "640c1f"},
		{"high_4_2", "64002a"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, _, sinkPad := buildPipeline(t, tc.plid, 45)
			n := runPipeline(t, p, sinkPad)
			if n <= 0 {
				t.Fatalf("no buffers received for plid=%s", tc.plid)
			}
		})
	}
}

// TestMaxResolutionSignal verifies the bin emits max-resolution once
// during caps negotiation with values consistent with the downstream
// plid's level.
func TestMaxResolutionSignal(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	cases := []struct {
		name     string
		plid     string
		wantMinW int
		wantMinH int
		wantMaxW int
		wantMaxH int
	}{
		// Level 3.1 @ 24fps (resolvePlid uses fps=24): maxFS=3600 MBs → ~1280x720
		{"high_3_1", "640c1f", 1200, 700, 1400, 800},
		// Level 4.2 @ 24fps: maxFS=8704 MBs → ~1920x1088
		{"high_4_2", "64002a", 1800, 1000, 2100, 1200},
		// Level 1.3 @ 24fps: 396 MBs → ~448x256
		{"baseline_1_3", "42c00d", 400, 200, 500, 300},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, bin, sinkPad := buildPipeline(t, tc.plid, 10)

			var gotW, gotH atomic.Int32
			var fireCount atomic.Int32
			if _, err := bin.Connect("max-resolution", func(_ *gst.Element, w, h int) {
				gotW.Store(int32(w))
				gotH.Store(int32(h))
				fireCount.Add(1)
			}); err != nil {
				t.Fatal("connect max-resolution:", err)
			}

			_ = runPipeline(t, p, sinkPad)

			fires := fireCount.Load()
			if fires != 1 {
				t.Errorf("plid=%s: expected exactly 1 max-resolution emission, got %d", tc.plid, fires)
			}
			w, h := int(gotW.Load()), int(gotH.Load())
			if w < tc.wantMinW || w > tc.wantMaxW || h < tc.wantMinH || h > tc.wantMaxH {
				t.Errorf("plid=%s: max-resolution %dx%d outside expected [%d..%d]x[%d..%d]",
					tc.plid, w, h, tc.wantMinW, tc.wantMaxW, tc.wantMinH, tc.wantMaxH)
			}
		})
	}
}

// TestMissingProfileLevelID confirms the bin tolerates a downstream
// capsfilter without profile-level-id: profile_capsfilter stays empty
// (x264enc picks its own default) and no max-resolution fires.
func TestMissingProfileLevelID(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	p, err := gst.NewPipeline("h264rtppaybin-no-plid")
	if err != nil {
		t.Fatal(err)
	}
	src, _ := gst.NewElementWithProperties("videotestsrc", map[string]interface{}{
		"num-buffers": 10,
		"is-live":     true,
	})
	rawCaps, _ := gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString("video/x-raw,width=320,height=240,framerate=15/1,format=I420"),
	})
	enc, _ := gst.NewElementWithProperties("x264enc", map[string]interface{}{
		"speed-preset": int(1),
	})
	bin, _ := gst.NewElementWithProperties("h264rtppaybin", map[string]interface{}{})
	// No profile-level-id in downstream caps.
	rtpCaps, _ := gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString("application/x-rtp, media=(string)video, encoding-name=(string)H264, clock-rate=(int)90000, payload=(int)96"),
	})
	sink, _ := gst.NewElementWithProperties("fakesink", map[string]interface{}{
		"sync":  false,
		"async": false,
	})

	if err := p.AddMany(src, rawCaps, enc, bin, rtpCaps, sink); err != nil {
		t.Fatal(err)
	}
	if err := gst.ElementLinkMany(src, rawCaps, enc, bin, rtpCaps, sink); err != nil {
		t.Fatal(err)
	}

	var fires atomic.Int32
	if _, err := bin.Connect("max-resolution", func(_ *gst.Element, _, _ int) {
		fires.Add(1)
	}); err != nil {
		t.Fatal(err)
	}

	n := runPipeline(t, p, sink.GetStaticPad("sink"))
	if n <= 0 {
		t.Fatal("no buffers received")
	}
	if fires.Load() != 0 {
		t.Errorf("expected no max-resolution emission without plid, got %d", fires.Load())
	}
}
