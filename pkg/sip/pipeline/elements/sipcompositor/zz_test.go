package sipcompositor_test

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/sipcompositor"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/testutils"
)

func TestMain(m *testing.M) {
	glib.SetEnv("GST_DEBUG", glib.GetEnv("GST_DEBUG")+",sip_compositor:5", true)
	gst.Init(nil)
	sipcompositor.Register()
	os.Exit(m.Run())
}

// --- Helpers ---

func testDebugDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join("testdata", t.Name())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("failed to create debug dir %s: %v", dir, err)
	}
	return dir
}

func dumpDot(t *testing.T, pipeline *gst.Pipeline, label string) {
	t.Helper()
	dir := testDebugDir(t)
	data := pipeline.Bin.DebugBinToDotData(gst.DebugGraphShowVerbose)
	path := filepath.Join(dir, label+".dot")
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Logf("WARNING: failed to write dot file %s: %v", path, err)
	}
}

func newTestPipeline(t *testing.T, name string) (*gst.Pipeline, *gst.Element) {
	t.Helper()
	pipeline, err := gst.NewPipeline(name)
	if err != nil {
		t.Fatal("failed to create pipeline:", err)
	}
	compositor, err := gst.NewElement("sip_compositor")
	if err != nil {
		t.Fatal("failed to create sip_compositor:", err)
	}
	if err := pipeline.Add(compositor); err != nil {
		t.Fatal("failed to add sip_compositor to pipeline:", err)
	}
	if err := pipeline.SetState(gst.StateReady); err != nil {
		t.Fatal("failed to set pipeline to READY:", err)
	}
	return pipeline, compositor
}

func addBufferProbe(t *testing.T, pad *gst.Pad) *atomic.Int32 {
	t.Helper()
	var count atomic.Int32
	pad.AddProbe(gst.PadProbeTypeBuffer|gst.PadProbeTypeBufferList, func(_ *gst.Pad, _ *gst.PadProbeInfo) gst.PadProbeReturn {
		count.Add(1)
		return gst.PadProbeOK
	})
	return &count
}

func waitForSrcPad(t *testing.T, compositor *gst.Element, padName string, timeout time.Duration) *gst.Pad {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		pad := compositor.GetStaticPad(padName)
		if pad != nil {
			return pad
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for sometimes pad %s", padName)
	return nil
}

// --- Tests ---

func TestSipCompositor_StateChanges(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, _ := newTestPipeline(t, "test-state-changes")
	pipeline.GetBus().SetFlushing(true)

	dumpDot(t, pipeline, "01_ready")

	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		t.Fatal("failed to set pipeline to PLAYING:", err)
	}
	dumpDot(t, pipeline, "02_playing")

	if err := pipeline.SetState(gst.StatePaused); err != nil {
		t.Fatal("failed to set pipeline to PAUSED:", err)
	}
	dumpDot(t, pipeline, "03_paused")

	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatal("failed to set pipeline to NULL:", err)
	}

	pipeline = nil
}

func TestSipCompositor_SinkPadRequest(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, compositor := newTestPipeline(t, "test-sink-pad-request")

	// Microphone pad (session=2, TrackSource_MICROPHONE)
	micPad := compositor.GetRequestPad("sink_2_1234_111")
	if micPad == nil {
		t.Fatal("GetRequestPad returned nil for microphone pad sink_2_1234_111")
	}

	// Camera pad (session=1, TrackSource_CAMERA)
	camPad := compositor.GetRequestPad("sink_1_5678_96")
	if camPad == nil {
		t.Fatal("GetRequestPad returned nil for camera pad sink_1_5678_96")
	}

	dumpDot(t, pipeline, "after_request")

	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatal("failed to set pipeline to NULL:", err)
	}
}

func TestSipCompositor_SinkPadRequest_InvalidNames(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, compositor := newTestPipeline(t, "test-sink-pad-invalid")

	if pad := compositor.GetRequestPad(""); pad != nil {
		t.Fatal("expected nil pad for empty name")
	}
	if pad := compositor.GetRequestPad("bad_name"); pad != nil {
		t.Fatal("expected nil pad for malformed name")
	}
	if pad := compositor.GetRequestPad("sink_99_1_1"); pad != nil {
		t.Fatal("expected nil pad for unknown session 99")
	}

	dumpDot(t, pipeline, "after_invalid_requests")

	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatal("failed to set pipeline to NULL:", err)
	}
}

func TestSipCompositor_SinkPadRelease(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, compositor := newTestPipeline(t, "test-sink-pad-release")

	micPad := compositor.GetRequestPad("sink_2_1234_111")
	if micPad == nil {
		t.Fatal("GetRequestPad returned nil for microphone pad")
	}

	camPad := compositor.GetRequestPad("sink_1_5678_96")
	if camPad == nil {
		t.Fatal("GetRequestPad returned nil for camera pad")
	}

	dumpDot(t, pipeline, "before_release")

	compositor.ReleaseRequestPad(micPad)
	compositor.ReleaseRequestPad(camPad)

	dumpDot(t, pipeline, "after_release")

	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatal("failed to set pipeline to NULL:", err)
	}
}

func TestSipCompositor_AudioMixing(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, compositor := newTestPipeline(t, "test-audio-mixing")

	// Create two audio sources (g711 raw + DTMF raw)
	audioSrc1, err := gst.NewElementWithProperties("audiotestsrc", map[string]any{"is-live": true, "freq": 440.0})
	if err != nil {
		t.Fatal("failed to create audiotestsrc 1:", err)
	}
	audioCaps1, err := gst.NewElement("capsfilter")
	if err != nil {
		t.Fatal("failed to create capsfilter 1:", err)
	}
	audioCaps1.SetProperty("caps", gst.NewCapsFromString("audio/x-raw,rate=48000,channels=1,format=S16LE"))

	audioSrc2, err := gst.NewElementWithProperties("audiotestsrc", map[string]any{"is-live": true, "freq": 880.0})
	if err != nil {
		t.Fatal("failed to create audiotestsrc 2:", err)
	}
	audioCaps2, err := gst.NewElement("capsfilter")
	if err != nil {
		t.Fatal("failed to create capsfilter 2:", err)
	}
	audioCaps2.SetProperty("caps", gst.NewCapsFromString("audio/x-raw,rate=48000,channels=1,format=S16LE"))

	// Audio sink
	audioSink, err := gst.NewElementWithProperties("fakesink", map[string]any{"sync": false})
	if err != nil {
		t.Fatal("failed to create audio fakesink:", err)
	}

	if err := pipeline.AddMany(audioSrc1, audioCaps1, audioSrc2, audioCaps2, audioSink); err != nil {
		t.Fatal("failed to add elements:", err)
	}
	if err := audioSrc1.Link(audioCaps1); err != nil {
		t.Fatal("failed to link audio chain 1:", err)
	}
	if err := audioSrc2.Link(audioCaps2); err != nil {
		t.Fatal("failed to link audio chain 2:", err)
	}

	// Request two microphone sink pads
	sinkPad1 := compositor.GetRequestPad("sink_2_1000_111")
	if sinkPad1 == nil {
		t.Fatal("GetRequestPad returned nil for mic pad 1")
	}
	sinkPad2 := compositor.GetRequestPad("sink_2_2000_112")
	if sinkPad2 == nil {
		t.Fatal("GetRequestPad returned nil for mic pad 2")
	}

	// Link sources to compositor
	if ret := audioCaps1.GetStaticPad("src").Link(sinkPad1); ret != gst.PadLinkOK {
		t.Fatal("failed to link audio 1 to compositor:", ret)
	}
	if ret := audioCaps2.GetStaticPad("src").Link(sinkPad2); ret != gst.PadLinkOK {
		t.Fatal("failed to link audio 2 to compositor:", ret)
	}

	// Wait for src pad and link sink
	var mu sync.Mutex
	linked := false
	audioSrcName := fmt.Sprintf("src_%d", livekit.TrackSource_MICROPHONE)

	compositor.Connect("pad-added", func(_ *gst.Element, pad *gst.Pad) {
		mu.Lock()
		defer mu.Unlock()
		if pad.GetName() == audioSrcName && !linked {
			if !audioSink.SyncStateWithParent() {
				t.Logf("warning: failed to sync audio fakesink state")
			}
			if ret := pad.Link(audioSink.GetStaticPad("sink")); ret != gst.PadLinkOK {
				t.Logf("warning: failed to link audio src pad: %v", ret)
				return
			}
			linked = true
		}
	})

	// Check if src pad already exists
	mu.Lock()
	if pad := compositor.GetStaticPad(audioSrcName); pad != nil && !linked {
		if !audioSink.SyncStateWithParent() {
			t.Logf("warning: failed to sync audio fakesink state")
		}
		if ret := pad.Link(audioSink.GetStaticPad("sink")); ret != gst.PadLinkOK {
			t.Logf("warning: failed to link audio src pad: %v", ret)
		} else {
			linked = true
		}
	}
	mu.Unlock()

	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		t.Fatal("failed to set pipeline to PLAYING:", err)
	}

	dumpDot(t, pipeline, "playing")

	bufCount := addBufferProbe(t, audioSink.GetStaticPad("sink"))

	time.Sleep(500 * time.Millisecond)

	if bufCount.Load() == 0 {
		t.Fatal("no buffers received on audio output after 500ms")
	}
	t.Logf("received %d audio buffers", bufCount.Load())

	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatal("failed to set pipeline to NULL:", err)
	}
}

func TestSipCompositor_CameraPassthrough(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, compositor := newTestPipeline(t, "test-camera-passthrough")

	// Create video source
	videoSrc, err := gst.NewElementWithProperties("videotestsrc", map[string]any{"is-live": true})
	if err != nil {
		t.Fatal("failed to create videotestsrc:", err)
	}
	videoCaps, err := gst.NewElement("capsfilter")
	if err != nil {
		t.Fatal("failed to create capsfilter:", err)
	}
	videoCaps.SetProperty("caps", gst.NewCapsFromString("video/x-raw,format=I420,width=320,height=240,framerate=15/1"))

	// Video sink
	videoSink, err := gst.NewElementWithProperties("fakesink", map[string]any{"sync": false})
	if err != nil {
		t.Fatal("failed to create video fakesink:", err)
	}

	if err := pipeline.AddMany(videoSrc, videoCaps, videoSink); err != nil {
		t.Fatal("failed to add elements:", err)
	}
	if err := videoSrc.Link(videoCaps); err != nil {
		t.Fatal("failed to link video chain:", err)
	}

	// Request camera sink pad
	sinkPad := compositor.GetRequestPad("sink_1_5678_96")
	if sinkPad == nil {
		t.Fatal("GetRequestPad returned nil for camera pad")
	}

	// Link source to compositor
	if ret := videoCaps.GetStaticPad("src").Link(sinkPad); ret != gst.PadLinkOK {
		t.Fatal("failed to link video to compositor:", ret)
	}

	// Wait for src pad and link sink
	var mu sync.Mutex
	linked := false
	videoSrcName := fmt.Sprintf("src_%d", livekit.TrackSource_CAMERA)

	compositor.Connect("pad-added", func(_ *gst.Element, pad *gst.Pad) {
		mu.Lock()
		defer mu.Unlock()
		if pad.GetName() == videoSrcName && !linked {
			if !videoSink.SyncStateWithParent() {
				t.Logf("warning: failed to sync video fakesink state")
			}
			if ret := pad.Link(videoSink.GetStaticPad("sink")); ret != gst.PadLinkOK {
				t.Logf("warning: failed to link video src pad: %v", ret)
				return
			}
			linked = true
		}
	})

	// Check if src pad already exists
	mu.Lock()
	if pad := compositor.GetStaticPad(videoSrcName); pad != nil && !linked {
		if !videoSink.SyncStateWithParent() {
			t.Logf("warning: failed to sync video fakesink state")
		}
		if ret := pad.Link(videoSink.GetStaticPad("sink")); ret != gst.PadLinkOK {
			t.Logf("warning: failed to link video src pad: %v", ret)
		} else {
			linked = true
		}
	}
	mu.Unlock()

	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		t.Fatal("failed to set pipeline to PLAYING:", err)
	}

	dumpDot(t, pipeline, "playing")

	bufCount := addBufferProbe(t, videoSink.GetStaticPad("sink"))

	time.Sleep(500 * time.Millisecond)

	if bufCount.Load() == 0 {
		t.Fatal("no buffers received on video output after 500ms")
	}
	t.Logf("received %d video buffers", bufCount.Load())

	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatal("failed to set pipeline to NULL:", err)
	}
}

func TestSipCompositor_AudioAndCamera(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, compositor := newTestPipeline(t, "test-audio-and-camera")

	// Audio source
	audioSrc, err := gst.NewElementWithProperties("audiotestsrc", map[string]any{"is-live": true})
	if err != nil {
		t.Fatal("failed to create audiotestsrc:", err)
	}
	audioCaps, err := gst.NewElement("capsfilter")
	if err != nil {
		t.Fatal("failed to create audio capsfilter:", err)
	}
	audioCaps.SetProperty("caps", gst.NewCapsFromString("audio/x-raw,rate=48000,channels=1,format=S16LE"))

	// Video source
	videoSrc, err := gst.NewElementWithProperties("videotestsrc", map[string]any{"is-live": true})
	if err != nil {
		t.Fatal("failed to create videotestsrc:", err)
	}
	videoCaps, err := gst.NewElement("capsfilter")
	if err != nil {
		t.Fatal("failed to create video capsfilter:", err)
	}
	videoCaps.SetProperty("caps", gst.NewCapsFromString("video/x-raw,format=I420,width=320,height=240,framerate=15/1"))

	// Sinks
	audioSink, err := gst.NewElementWithProperties("fakesink", map[string]any{"sync": false})
	if err != nil {
		t.Fatal("failed to create audio fakesink:", err)
	}
	videoSink, err := gst.NewElementWithProperties("fakesink", map[string]any{"sync": false})
	if err != nil {
		t.Fatal("failed to create video fakesink:", err)
	}

	if err := pipeline.AddMany(audioSrc, audioCaps, videoSrc, videoCaps, audioSink, videoSink); err != nil {
		t.Fatal("failed to add elements:", err)
	}
	if err := audioSrc.Link(audioCaps); err != nil {
		t.Fatal("failed to link audio chain:", err)
	}
	if err := videoSrc.Link(videoCaps); err != nil {
		t.Fatal("failed to link video chain:", err)
	}

	// Request pads
	micSinkPad := compositor.GetRequestPad("sink_2_1000_111")
	if micSinkPad == nil {
		t.Fatal("GetRequestPad returned nil for mic pad")
	}
	camSinkPad := compositor.GetRequestPad("sink_1_5000_96")
	if camSinkPad == nil {
		t.Fatal("GetRequestPad returned nil for camera pad")
	}

	// Link sources
	if ret := audioCaps.GetStaticPad("src").Link(micSinkPad); ret != gst.PadLinkOK {
		t.Fatal("failed to link audio to compositor:", ret)
	}
	if ret := videoCaps.GetStaticPad("src").Link(camSinkPad); ret != gst.PadLinkOK {
		t.Fatal("failed to link video to compositor:", ret)
	}

	// Link src pads to sinks
	audioSrcName := fmt.Sprintf("src_%d", livekit.TrackSource_MICROPHONE)
	videoSrcName := fmt.Sprintf("src_%d", livekit.TrackSource_CAMERA)

	var mu sync.Mutex
	linked := make(map[string]bool)

	tryLink := func(pad *gst.Pad) {
		name := pad.GetName()
		if linked[name] {
			return
		}
		switch name {
		case audioSrcName:
			if !audioSink.SyncStateWithParent() {
				t.Logf("warning: failed to sync audio fakesink state")
			}
			if ret := pad.Link(audioSink.GetStaticPad("sink")); ret != gst.PadLinkOK {
				t.Logf("warning: failed to link audio src pad: %v", ret)
				return
			}
			linked[name] = true
		case videoSrcName:
			if !videoSink.SyncStateWithParent() {
				t.Logf("warning: failed to sync video fakesink state")
			}
			if ret := pad.Link(videoSink.GetStaticPad("sink")); ret != gst.PadLinkOK {
				t.Logf("warning: failed to link video src pad: %v", ret)
				return
			}
			linked[name] = true
		}
	}

	compositor.Connect("pad-added", func(_ *gst.Element, pad *gst.Pad) {
		mu.Lock()
		defer mu.Unlock()
		tryLink(pad)
	})

	mu.Lock()
	if pad := compositor.GetStaticPad(audioSrcName); pad != nil {
		tryLink(pad)
	}
	if pad := compositor.GetStaticPad(videoSrcName); pad != nil {
		tryLink(pad)
	}
	mu.Unlock()

	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		t.Fatal("failed to set pipeline to PLAYING:", err)
	}

	dumpDot(t, pipeline, "playing")

	audioBufCount := addBufferProbe(t, audioSink.GetStaticPad("sink"))
	videoBufCount := addBufferProbe(t, videoSink.GetStaticPad("sink"))

	time.Sleep(500 * time.Millisecond)

	if audioBufCount.Load() == 0 {
		t.Fatal("no audio buffers received after 500ms")
	}
	if videoBufCount.Load() == 0 {
		t.Fatal("no video buffers received after 500ms")
	}
	t.Logf("received %d audio buffers, %d video buffers", audioBufCount.Load(), videoBufCount.Load())

	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatal("failed to set pipeline to NULL:", err)
	}
}
