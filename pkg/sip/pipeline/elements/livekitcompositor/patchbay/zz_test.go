package patchbay_test

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/livekitcompositor/patchbay"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/testutils"
)

func init() {
	patchbay.CAT = gst.NewDebugCategory(
		"livekit_patchbay",
		gst.DebugColorNone,
		"LiveKit Patchbay Element",
	)
}

func TestMain(m *testing.M) {
	glib.SetEnv("GST_DEBUG", glib.GetEnv("GST_DEBUG")+",livekit_patchbay:5", true)
	gst.Init(nil)
	gst.RegisterElement(nil, "livekit_patchbay", gst.RankNone, &patchbay.Patchbay{}, gst.ExtendsBin)
	os.Exit(m.Run())
}

// --- Helpers ---

// testDebugDir returns a per-test debug output directory, creating it if needed.
func testDebugDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join("testdata", t.Name())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("failed to create debug dir %s: %v", dir, err)
	}
	return dir
}

// dumpDot writes a dot graph of the pipeline to the test debug directory.
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
	pb, err := gst.NewElement("livekit_patchbay")
	if err != nil {
		t.Fatal("failed to create patchbay:", err)
	}
	if err := pipeline.Add(pb); err != nil {
		t.Fatal("failed to add patchbay to pipeline:", err)
	}
	return pipeline, pb
}

func requirePad(t *testing.T, elem *gst.Element, name string) *gst.Pad {
	t.Helper()
	pad := elem.GetRequestPad(name)
	if pad == nil {
		t.Fatalf("expected non-nil pad for %q", name)
	}
	return pad
}

func addBufferProbe(t *testing.T, element *gst.Element) *atomic.Int32 {
	t.Helper()
	var count atomic.Int32
	sinkPad := element.GetStaticPad("sink")
	if sinkPad == nil {
		t.Fatal("failed to get sink pad for buffer probe")
	}
	sinkPad.AddProbe(gst.PadProbeTypeBuffer|gst.PadProbeTypeBufferList, func(_ *gst.Pad, _ *gst.PadProbeInfo) gst.PadProbeReturn {
		count.Add(1)
		return gst.PadProbeOK
	})
	return &count
}

func busLoop(t *testing.T, pipeline *gst.Pipeline, deadline time.Duration) bool {
	t.Helper()
	bus := pipeline.GetPipelineBus()
	timeout := gst.ClockTime(time.Second)
	end := time.Now().Add(deadline)

	for time.Now().Before(end) {
		msg := bus.TimedPop(timeout)
		if msg == nil {
			continue
		}
		switch msg.Type() {
		case gst.MessageEOS:
			return true
		case gst.MessageError:
			gerr := msg.ParseError()
			t.Fatal("pipeline error:", gerr.Error())
		}
	}
	return false
}

// linkToVideoFile creates a videoconvert → x264enc → matroskamux → filesink chain.
// Attaches a buffer probe on the videoconvert sink pad and returns the counter.
func linkToVideoFile(t *testing.T, pipeline *gst.Pipeline, srcPad *gst.Pad, filename string) *atomic.Int32 {
	t.Helper()
	dir := testDebugDir(t)
	path := filepath.Join(dir, filename)

	convert, err := gst.NewElement("videoconvert")
	if err != nil {
		t.Fatal("failed to create videoconvert:", err)
	}
	enc, err := gst.NewElementWithProperties("x264enc", map[string]any{
		"speed-preset": 1,   // ultrafast
		"tune":         0x4, // zerolatency
		"key-int-max":  30,
	})
	if err != nil {
		t.Fatal("failed to create x264enc:", err)
	}
	mux, err := gst.NewElement("matroskamux")
	if err != nil {
		t.Fatal("failed to create matroskamux:", err)
	}
	sink, err := gst.NewElementWithProperties("filesink", map[string]any{
		"location": path,
		"sync":     false,
		"async":    false,
	})
	if err != nil {
		t.Fatal("failed to create filesink:", err)
	}

	for _, e := range []*gst.Element{convert, enc, mux, sink} {
		if err := pipeline.Add(e); err != nil {
			t.Fatalf("failed to add element to pipeline: %v", err)
		}
	}
	if err := gst.ElementLinkMany(convert, enc, mux, sink); err != nil {
		t.Fatal("failed to link video encoding chain:", err)
	}

	convertSink := convert.GetStaticPad("sink")
	if ret := srcPad.Link(convertSink); ret != gst.PadLinkOK {
		t.Fatalf("failed to link to video encoding chain: %v", ret)
	}

	for _, e := range []*gst.Element{convert, enc, mux, sink} {
		if !e.SyncStateWithParent() {
			t.Fatalf("failed to sync %s state with parent", e.GetName())
		}
	}

	count := addBufferProbe(t, convert)
	return count
}

// videotestsrcColors maps color indices to distinct foreground/background ARGB pairs.
var videotestsrcColors = [][2]uint32{
	{0xFFFFFFFF, 0xFF000000}, // white ball on black
	{0xFFFF0000, 0xFF0000FF}, // red ball on blue
	{0xFF00FF00, 0xFFFF00FF}, // green ball on magenta
}

// linkFromVideotestsrc creates a videotestsrc (ball pattern) with distinct colors per colorIndex.
func linkFromVideotestsrc(t *testing.T, pipeline *gst.Pipeline, sinkPad *gst.Pad, colorIndex int, numBuffers int) *gst.Element {
	t.Helper()
	colors := videotestsrcColors[colorIndex%len(videotestsrcColors)]
	props := map[string]any{
		"is-live":          true,
		"pattern":          18, // ball
		"foreground-color": colors[0],
		"background-color": colors[1],
	}
	if numBuffers > 0 {
		props["num-buffers"] = numBuffers
	}
	src, err := gst.NewElementWithProperties("videotestsrc", props)
	if err != nil {
		t.Fatal("failed to create videotestsrc:", err)
	}
	capsFilter, err := gst.NewElement("capsfilter")
	if err != nil {
		t.Fatal("failed to create capsfilter:", err)
	}
	if err := capsFilter.SetProperty("caps", gst.NewCapsFromString("video/x-raw,width=320,height=240,framerate=15/1")); err != nil {
		t.Fatal("failed to set caps:", err)
	}

	if err := pipeline.Add(src); err != nil {
		t.Fatal("failed to add videotestsrc:", err)
	}
	if err := pipeline.Add(capsFilter); err != nil {
		t.Fatal("failed to add capsfilter:", err)
	}
	if err := src.Link(capsFilter); err != nil {
		t.Fatal("failed to link videotestsrc to capsfilter:", err)
	}

	filterSrc := capsFilter.GetStaticPad("src")
	if ret := filterSrc.Link(sinkPad); ret != gst.PadLinkOK {
		t.Fatalf("failed to link capsfilter to patchbay pad: %v", ret)
	}

	if !src.SyncStateWithParent() {
		t.Fatal("failed to sync videotestsrc state with parent")
	}
	if !capsFilter.SyncStateWithParent() {
		t.Fatal("failed to sync capsfilter state with parent")
	}
	return src
}

// activatePath emits the activate-path signal on the patchbay element.
func activatePath(t *testing.T, pb *gst.Element, sinkPad, srcPad *gst.Pad) {
	t.Helper()
	sinkGhost := sinkPad.AsGhostPad()
	srcGhost := srcPad.AsGhostPad()
	if sinkGhost == nil {
		t.Fatal("sinkPad is not a ghost pad")
	}
	if srcGhost == nil {
		t.Fatal("srcPad is not a ghost pad")
	}
	_, err := pb.Emit("activate-path", sinkGhost, srcGhost)
	if err != nil {
		t.Fatalf("failed to emit activate-path: %v", err)
	}
}

// --- Group 1: Pad Management ---

func TestPatchbay_RequestSourcePad(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, pb := newTestPipeline(t, "test-request-src")

	src0 := requirePad(t, pb, "src_%u")
	if src0.GetName() != "src_0" {
		t.Fatalf("expected src_0, got %s", src0.GetName())
	}

	src1 := requirePad(t, pb, "src_%u")
	if src1.GetName() != "src_1" {
		t.Fatalf("expected src_1, got %s", src1.GetName())
	}

	dumpDot(t, pipeline, "after_request")

	if err := pipeline.SetState(gst.StateReady); err != nil {
		t.Fatal("failed to set pipeline to READY:", err)
	}
	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatal("failed to set pipeline to NULL:", err)
	}
}

func TestPatchbay_RequestSinkPadRequiresSource(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, pb := newTestPipeline(t, "test-sink-requires-src")

	pad := pb.GetRequestPad("sink_%u")
	if pad != nil {
		t.Fatal("expected nil pad when no sources exist")
	}

	dumpDot(t, pipeline, "after_request_denied")

	if err := pipeline.SetState(gst.StateReady); err != nil {
		t.Fatal("failed to set pipeline to READY:", err)
	}
	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatal("failed to set pipeline to NULL:", err)
	}
}

func TestPatchbay_RequestSinkPadAfterSource(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, pb := newTestPipeline(t, "test-sink-after-src")

	_ = requirePad(t, pb, "src_%u")

	sink0 := requirePad(t, pb, "sink_%u")
	if sink0.GetName() != "sink_0" {
		t.Fatalf("expected sink_0, got %s", sink0.GetName())
	}

	dumpDot(t, pipeline, "after_request")

	if err := pipeline.SetState(gst.StateReady); err != nil {
		t.Fatal("failed to set pipeline to READY:", err)
	}
	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatal("failed to set pipeline to NULL:", err)
	}
}

func TestPatchbay_MultipleSinksAndSources(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, pb := newTestPipeline(t, "test-multi-sink-src")

	src0 := requirePad(t, pb, "src_%u")
	src1 := requirePad(t, pb, "src_%u")
	sink0 := requirePad(t, pb, "sink_%u")
	sink1 := requirePad(t, pb, "sink_%u")

	if src0.GetName() != "src_0" || src1.GetName() != "src_1" {
		t.Fatalf("unexpected source pad names: %s, %s", src0.GetName(), src1.GetName())
	}
	if sink0.GetName() != "sink_0" || sink1.GetName() != "sink_1" {
		t.Fatalf("unexpected sink pad names: %s, %s", sink0.GetName(), sink1.GetName())
	}

	dumpDot(t, pipeline, "after_request")

	if err := pipeline.SetState(gst.StateReady); err != nil {
		t.Fatal("failed to set pipeline to READY:", err)
	}
	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatal("failed to set pipeline to NULL:", err)
	}
}

func TestPatchbay_ReleaseSinkPad(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, pb := newTestPipeline(t, "test-release-sink")

	_ = requirePad(t, pb, "src_%u")

	sink0 := requirePad(t, pb, "sink_%u")
	_ = requirePad(t, pb, "sink_%u")

	dumpDot(t, pipeline, "before_release")

	pb.ReleaseRequestPad(sink0)

	dumpDot(t, pipeline, "after_release_sink0")

	// Request a new sink — should reuse slot 0
	newSink := requirePad(t, pb, "sink_%u")
	if newSink.GetName() != "sink_0" {
		t.Fatalf("expected slot reuse (sink_0), got %s", newSink.GetName())
	}

	dumpDot(t, pipeline, "after_reuse")

	if err := pipeline.SetState(gst.StateReady); err != nil {
		t.Fatal("failed to set pipeline to READY:", err)
	}
	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatal("failed to set pipeline to NULL:", err)
	}
}

func TestPatchbay_ReleaseSourcePadGuard(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, pb := newTestPipeline(t, "test-release-src-guard")

	src0 := requirePad(t, pb, "src_%u")
	_ = requirePad(t, pb, "sink_%u")

	// Try to release the only source while a sink exists — should be blocked
	pb.ReleaseRequestPad(src0)

	dumpDot(t, pipeline, "after_blocked_release")

	// Verify the source pad is still there (release was blocked)
	if pb.GetStaticPad("src_0") == nil {
		t.Fatal("source pad should not have been released while sinks exist")
	}

	if err := pipeline.SetState(gst.StateReady); err != nil {
		t.Fatal("failed to set pipeline to READY:", err)
	}
	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatal("failed to set pipeline to NULL:", err)
	}
}

func TestPatchbay_RequestUnknownPad(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, pb := newTestPipeline(t, "test-unknown-pad")

	pad := pb.GetRequestPad("unknown_%u")
	if pad != nil {
		t.Fatal("expected nil for unknown pad template")
	}

	dumpDot(t, pipeline, "after_unknown_request")

	if err := pipeline.SetState(gst.StateReady); err != nil {
		t.Fatal("failed to set pipeline to READY:", err)
	}
	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatal("failed to set pipeline to NULL:", err)
	}
}

// --- Group 2: Data Flow ---

func TestPatchbay_BasicDataFlow(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, pb := newTestPipeline(t, "test-basic-flow")

	srcPad := requirePad(t, pb, "src_%u")
	sinkPad := requirePad(t, pb, "sink_%u")

	linkFromVideotestsrc(t, pipeline, sinkPad, 0, -1) // white ball on black
	count := linkToVideoFile(t, pipeline, srcPad, "src_0.mkv")

	activatePath(t, pb, sinkPad, srcPad)

	dumpDot(t, pipeline, "01_built")

	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		t.Fatal("failed to set pipeline to PLAYING:", err)
	}

	dumpDot(t, pipeline, "02_playing")

	time.Sleep(2 * time.Second)

	dumpDot(t, pipeline, "03_before_null")

	c := count.Load()
	t.Logf("received %d buffers", c)
	if c <= 0 {
		t.Fatal("no buffers received through patchbay")
	}

	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatal("failed to set pipeline to NULL:", err)
	}
}

func TestPatchbay_MultiPathDataFlow(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, pb := newTestPipeline(t, "test-multi-path")

	src0 := requirePad(t, pb, "src_%u")
	src1 := requirePad(t, pb, "src_%u")
	sink0 := requirePad(t, pb, "sink_%u")
	sink1 := requirePad(t, pb, "sink_%u")

	linkFromVideotestsrc(t, pipeline, sink0, 0, -1) // white ball on black
	linkFromVideotestsrc(t, pipeline, sink1, 1, -1) // red ball on blue
	count0 := linkToVideoFile(t, pipeline, src0, "src_0.mkv")
	count1 := linkToVideoFile(t, pipeline, src1, "src_1.mkv")

	activatePath(t, pb, sink0, src0)
	activatePath(t, pb, sink1, src1)

	dumpDot(t, pipeline, "01_built")

	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		t.Fatal("failed to set pipeline to PLAYING:", err)
	}

	dumpDot(t, pipeline, "02_playing")

	time.Sleep(2 * time.Second)

	dumpDot(t, pipeline, "03_before_null")

	c0, c1 := count0.Load(), count1.Load()
	t.Logf("src_0: %d buffers, src_1: %d buffers", c0, c1)
	if c0 <= 0 {
		t.Fatal("no buffers received on src_0")
	}
	if c1 <= 0 {
		t.Fatal("no buffers received on src_1")
	}

	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatal("failed to set pipeline to NULL:", err)
	}
}

func TestPatchbay_ActivatePathSwitchesRouting(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, pb := newTestPipeline(t, "test-switch-routing")

	src0 := requirePad(t, pb, "src_%u")
	src1 := requirePad(t, pb, "src_%u")
	sink0 := requirePad(t, pb, "sink_%u")

	linkFromVideotestsrc(t, pipeline, sink0, 0, -1) // white ball on black
	count0 := linkToVideoFile(t, pipeline, src0, "src_0.mkv")
	count1 := linkToVideoFile(t, pipeline, src1, "src_1.mkv")

	// Initially route sink_0 → src_0
	activatePath(t, pb, sink0, src0)

	dumpDot(t, pipeline, "01_built_route_src0")

	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		t.Fatal("failed to set pipeline to PLAYING:", err)
	}

	dumpDot(t, pipeline, "02_playing")

	time.Sleep(1 * time.Second)

	before0 := count0.Load()
	before1 := count1.Load()
	t.Logf("before switch: src_0=%d, src_1=%d", before0, before1)

	if before0 <= 0 {
		t.Fatal("expected buffers on src_0 before switch")
	}

	dumpDot(t, pipeline, "03_before_switch")

	// Switch to sink_0 → src_1
	activatePath(t, pb, sink0, src1)

	dumpDot(t, pipeline, "04_after_switch")

	time.Sleep(1 * time.Second)

	after0 := count0.Load()
	after1 := count1.Load()
	t.Logf("after switch: src_0=%d, src_1=%d", after0, after1)

	dumpDot(t, pipeline, "05_before_null")

	delta1 := after1 - before1
	if delta1 <= 0 {
		t.Fatalf("expected buffers on src_1 after switch, delta=%d", delta1)
	}

	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatal("failed to set pipeline to NULL:", err)
	}
}

// --- Group 3: Stalled Source Recovery ---

func TestPatchbay_StalledSourceNoDeadlock(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, pb := newTestPipeline(t, "test-stalled-no-deadlock")

	srcPad := requirePad(t, pb, "src_%u")
	sinkPad := requirePad(t, pb, "sink_%u")

	linkFromVideotestsrc(t, pipeline, sinkPad, 0, 10) // white ball on black, 10 buffers
	count := linkToVideoFile(t, pipeline, srcPad, "src_0.mkv")

	activatePath(t, pb, sinkPad, srcPad)

	dumpDot(t, pipeline, "01_built")

	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		t.Fatal("failed to set pipeline to PLAYING:", err)
	}

	dumpDot(t, pipeline, "02_playing")

	// If there's a deadlock, the test will timeout
	time.Sleep(3 * time.Second)

	dumpDot(t, pipeline, "03_after_stall")

	c := count.Load()
	t.Logf("received %d buffers from finite source", c)
	if c <= 0 {
		t.Fatal("no buffers received")
	}

	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatal("failed to set pipeline to NULL:", err)
	}
}

func TestPatchbay_StalledSourceOtherPathContinues(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, pb := newTestPipeline(t, "test-stalled-other-continues")

	src0 := requirePad(t, pb, "src_%u")
	src1 := requirePad(t, pb, "src_%u")
	sink0 := requirePad(t, pb, "sink_%u")
	sink1 := requirePad(t, pb, "sink_%u")

	// sink_0: finite source (stops after 10 buffers)
	linkFromVideotestsrc(t, pipeline, sink0, 0, 10) // white ball on black
	// sink_1: infinite source
	linkFromVideotestsrc(t, pipeline, sink1, 1, -1) // red ball on blue

	count0 := linkToVideoFile(t, pipeline, src0, "src_0_finite.mkv")
	count1 := linkToVideoFile(t, pipeline, src1, "src_1_infinite.mkv")

	activatePath(t, pb, sink0, src0)
	activatePath(t, pb, sink1, src1)

	dumpDot(t, pipeline, "01_built")

	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		t.Fatal("failed to set pipeline to PLAYING:", err)
	}

	dumpDot(t, pipeline, "02_playing")

	// Wait for finite source to finish
	time.Sleep(3 * time.Second)

	dumpDot(t, pipeline, "03_after_stall")

	c1 := count1.Load()

	// Verify src_1 keeps flowing after src_0 stalled
	time.Sleep(1 * time.Second)
	c1After := count1.Load()

	dumpDot(t, pipeline, "04_before_null")

	c0 := count0.Load()
	t.Logf("src_0 (finite): %d buffers, src_1 (infinite): %d→%d buffers", c0, c1, c1After)

	if c0 <= 0 {
		t.Fatal("no buffers received on src_0 (finite)")
	}
	if c1After <= c1 {
		t.Fatal("src_1 (infinite) stopped producing buffers after src_0 stalled")
	}

	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatal("failed to set pipeline to NULL:", err)
	}
}

// --- Group 4: Pad Request/Release While PLAYING ---

func TestPatchbay_RequestSinkPadWhilePlaying(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, pb := newTestPipeline(t, "test-req-sink-playing")

	srcPad := requirePad(t, pb, "src_%u")
	sinkPad := requirePad(t, pb, "sink_%u")

	linkFromVideotestsrc(t, pipeline, sinkPad, 0, -1) // white ball on black
	count0 := linkToVideoFile(t, pipeline, srcPad, "src_0.mkv")

	activatePath(t, pb, sinkPad, srcPad)

	dumpDot(t, pipeline, "01_built")

	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		t.Fatal("failed to set pipeline to PLAYING:", err)
	}

	dumpDot(t, pipeline, "02_playing")

	time.Sleep(1 * time.Second)
	if count0.Load() <= 0 {
		t.Fatal("no buffers before dynamic pad request")
	}

	// Request new sink while PLAYING
	sink1 := requirePad(t, pb, "sink_%u")
	linkFromVideotestsrc(t, pipeline, sink1, 1, -1) // red ball on blue

	dumpDot(t, pipeline, "03_after_new_sink")

	// Switch routing to new sink
	activatePath(t, pb, sink1, srcPad)

	dumpDot(t, pipeline, "04_after_switch")

	time.Sleep(1 * time.Second)

	dumpDot(t, pipeline, "05_before_null")

	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatal("failed to set pipeline to NULL:", err)
	}
}

func TestPatchbay_RequestSourcePadWhilePlaying(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, pb := newTestPipeline(t, "test-req-src-playing")

	srcPad := requirePad(t, pb, "src_%u")
	sinkPad := requirePad(t, pb, "sink_%u")

	linkFromVideotestsrc(t, pipeline, sinkPad, 0, -1) // white ball on black
	count0 := linkToVideoFile(t, pipeline, srcPad, "src_0.mkv")

	activatePath(t, pb, sinkPad, srcPad)

	dumpDot(t, pipeline, "01_built")

	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		t.Fatal("failed to set pipeline to PLAYING:", err)
	}

	dumpDot(t, pipeline, "02_playing")

	time.Sleep(1 * time.Second)
	if count0.Load() <= 0 {
		t.Fatal("no buffers before dynamic pad request")
	}

	// Request new source while PLAYING
	src1 := requirePad(t, pb, "src_%u")
	count1 := linkToVideoFile(t, pipeline, src1, "src_1.mkv")

	dumpDot(t, pipeline, "03_after_new_source")

	// Switch routing to new source
	activatePath(t, pb, sinkPad, src1)

	dumpDot(t, pipeline, "04_after_switch")

	time.Sleep(1 * time.Second)

	dumpDot(t, pipeline, "05_before_null")

	c1 := count1.Load()
	t.Logf("new source received %d buffers after switch", c1)
	if c1 <= 0 {
		t.Fatal("no buffers received on dynamically added source pad")
	}

	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatal("failed to set pipeline to NULL:", err)
	}
}

func TestPatchbay_ReleaseSinkPadWhilePlaying(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, pb := newTestPipeline(t, "test-release-sink-playing")

	srcPad := requirePad(t, pb, "src_%u")
	sink0 := requirePad(t, pb, "sink_%u")
	sink1 := requirePad(t, pb, "sink_%u")

	linkFromVideotestsrc(t, pipeline, sink0, 0, -1) // white ball on black
	src1 := linkFromVideotestsrc(t, pipeline, sink1, 1, -1) // red ball on blue
	count := linkToVideoFile(t, pipeline, srcPad, "src_0.mkv")

	// Activate only sink_0 → src_0. sink_1 is inactive.
	activatePath(t, pb, sink0, srcPad)

	dumpDot(t, pipeline, "01_built")

	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		t.Fatal("failed to set pipeline to PLAYING:", err)
	}

	dumpDot(t, pipeline, "02_playing")

	time.Sleep(1 * time.Second)
	before := count.Load()
	if before <= 0 {
		t.Fatal("no buffers before release")
	}

	dumpDot(t, pipeline, "03_before_release")

	// Stop the videotestsrc connected to sink_1 before releasing the pad
	if err := src1.SetState(gst.StateNull); err != nil {
		t.Fatalf("failed to stop videotestsrc for sink_1: %v", err)
	}

	// Release the inactive sink pad while PLAYING
	pb.ReleaseRequestPad(sink1)

	dumpDot(t, pipeline, "04_after_release")

	time.Sleep(1 * time.Second)
	after := count.Load()

	dumpDot(t, pipeline, "05_before_null")

	t.Logf("before release: %d, after release: %d", before, after)
	if after <= before {
		t.Fatal("active path stopped flowing after releasing inactive sink")
	}

	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatal("failed to set pipeline to NULL:", err)
	}
}

func TestPatchbay_ReleaseSourcePadWhilePlaying(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, pb := newTestPipeline(t, "test-release-src-playing")

	src0 := requirePad(t, pb, "src_%u")
	src1 := requirePad(t, pb, "src_%u")
	sink0 := requirePad(t, pb, "sink_%u")

	linkFromVideotestsrc(t, pipeline, sink0, 0, -1) // white ball on black
	count := linkToVideoFile(t, pipeline, src0, "src_0.mkv")
	linkToVideoFile(t, pipeline, src1, "src_1.mkv")

	// Activate only sink_0 → src_0
	activatePath(t, pb, sink0, src0)

	dumpDot(t, pipeline, "01_built")

	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		t.Fatal("failed to set pipeline to PLAYING:", err)
	}

	dumpDot(t, pipeline, "02_playing")

	time.Sleep(1 * time.Second)
	before := count.Load()
	if before <= 0 {
		t.Fatal("no buffers before release")
	}

	dumpDot(t, pipeline, "03_before_release")

	// Release src_1 (not the active path, and there's still src_0 for the sink)
	pb.ReleaseRequestPad(src1)

	dumpDot(t, pipeline, "04_after_release")

	time.Sleep(1 * time.Second)
	after := count.Load()

	dumpDot(t, pipeline, "05_before_null")

	t.Logf("before release: %d, after release: %d", before, after)
	if after <= before {
		t.Fatal("active path stopped flowing after releasing unused source")
	}

	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatal("failed to set pipeline to NULL:", err)
	}
}

// --- Group 5: State Changes ---

func TestPatchbay_StateChanges(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, _ := newTestPipeline(t, "test-state-changes")

	if err := pipeline.SetState(gst.StateReady); err != nil {
		t.Fatal("failed to set pipeline to READY:", err)
	}
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
}

func TestPatchbay_StateChangeWithPads(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, pb := newTestPipeline(t, "test-state-with-pads")

	_ = requirePad(t, pb, "src_%u")
	_ = requirePad(t, pb, "sink_%u")

	dumpDot(t, pipeline, "01_with_pads")

	if err := pipeline.SetState(gst.StateReady); err != nil {
		t.Fatal("failed to set pipeline to READY:", err)
	}

	dumpDot(t, pipeline, "02_ready")

	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatal("failed to set pipeline to NULL:", err)
	}

	dumpDot(t, pipeline, "03_after_null")

	// After NULL, internal state should be cleared by ChangeState(ReadyToNull).
	// Verify: requesting a sink without source should fail (sources were cleared).
	if err := pipeline.SetState(gst.StateReady); err != nil {
		t.Fatal("failed to set pipeline back to READY:", err)
	}

	pad := pb.GetRequestPad("sink_%u")
	if pad != nil {
		t.Fatal("expected nil sink pad after state cleared (no sources)")
	}

	dumpDot(t, pipeline, "04_ready_no_sources")

	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatal("failed to set pipeline to NULL:", err)
	}
}

// --- Group 6: activate-path Edge Cases ---

func TestPatchbay_ActivatePathIdempotent(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, pb := newTestPipeline(t, "test-activate-idempotent")

	srcPad := requirePad(t, pb, "src_%u")
	sinkPad := requirePad(t, pb, "sink_%u")

	linkFromVideotestsrc(t, pipeline, sinkPad, 0, -1) // white ball on black
	count := linkToVideoFile(t, pipeline, srcPad, "src_0.mkv")

	// Activate same path twice before playing
	activatePath(t, pb, sinkPad, srcPad)
	activatePath(t, pb, sinkPad, srcPad)

	dumpDot(t, pipeline, "01_built_double_activate")

	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		t.Fatal("failed to set pipeline to PLAYING:", err)
	}

	dumpDot(t, pipeline, "02_playing")

	time.Sleep(2 * time.Second)

	// Activate again while playing
	activatePath(t, pb, sinkPad, srcPad)

	dumpDot(t, pipeline, "03_after_third_activate")

	time.Sleep(1 * time.Second)

	dumpDot(t, pipeline, "04_before_null")

	c := count.Load()
	t.Logf("received %d buffers", c)
	if c <= 0 {
		t.Fatal("no buffers received after idempotent activate-path")
	}

	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatal("failed to set pipeline to NULL:", err)
	}
}

// --- Group 7: Integration ---

func TestPatchbay_RealisticSession(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, pb := newTestPipeline(t, "test-realistic-session")

	// Step 1: Start with 1 source + 1 sink (first participant joins)
	src0 := requirePad(t, pb, "src_%u")
	sink0 := requirePad(t, pb, "sink_%u")

	linkFromVideotestsrc(t, pipeline, sink0, 0, -1) // white ball on black
	countSrc0 := linkToVideoFile(t, pipeline, src0, "src_0.mkv")

	activatePath(t, pb, sink0, src0)

	dumpDot(t, pipeline, "01_one_participant")

	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		t.Fatal("failed to set pipeline to PLAYING:", err)
	}

	dumpDot(t, pipeline, "02_playing")

	time.Sleep(1 * time.Second)
	c := countSrc0.Load()
	t.Logf("step 1: src_0 has %d buffers", c)
	if c <= 0 {
		t.Fatal("no buffers on src_0 after initial setup")
	}

	// Step 2: Second participant joins (add src_1 + sink_1 while PLAYING)
	src1 := requirePad(t, pb, "src_%u")
	sink1 := requirePad(t, pb, "sink_%u")

	linkFromVideotestsrc(t, pipeline, sink1, 1, -1) // red ball on blue
	countSrc1 := linkToVideoFile(t, pipeline, src1, "src_1.mkv")

	activatePath(t, pb, sink1, src1)

	dumpDot(t, pipeline, "03_two_participants")

	time.Sleep(1 * time.Second)
	c1 := countSrc1.Load()
	t.Logf("step 2: src_0=%d, src_1=%d", countSrc0.Load(), c1)
	if c1 <= 0 {
		t.Fatal("no buffers on src_1 after second participant joined")
	}

	// Step 3: Third sink joins but reuses an existing source (no new source added)
	sink2 := requirePad(t, pb, "sink_%u")
	linkFromVideotestsrc(t, pipeline, sink2, 2, -1) // green ball on magenta

	// Route sink_2 to src_0 (shares output with first participant's source)
	activatePath(t, pb, sink2, src0)

	dumpDot(t, pipeline, "04_third_sink_added")

	time.Sleep(1 * time.Second)
	beforeSwitch0 := countSrc0.Load()
	beforeSwitch1 := countSrc1.Load()
	t.Logf("step 3: src_0=%d, src_1=%d", beforeSwitch0, beforeSwitch1)

	// Step 4: Active speaker changes — switch sink_0 to src_1
	activatePath(t, pb, sink0, src1)

	dumpDot(t, pipeline, "05_switch_sink0_to_src1")

	time.Sleep(1 * time.Second)
	afterSwitch1 := countSrc1.Load()
	t.Logf("step 4 (sink0→src1): src_0=%d, src_1=%d", countSrc0.Load(), afterSwitch1)

	delta1 := afterSwitch1 - beforeSwitch1
	if delta1 <= 0 {
		t.Fatalf("src_1 should keep receiving after switch, delta=%d", delta1)
	}

	// Step 5: Switch sink_0 back to src_0
	activatePath(t, pb, sink0, src0)

	dumpDot(t, pipeline, "06_switch_sink0_back_to_src0")

	time.Sleep(1 * time.Second)

	// Step 6: Switch sink_2 to src_1
	activatePath(t, pb, sink2, src1)

	dumpDot(t, pipeline, "07_switch_sink2_to_src1")

	time.Sleep(1 * time.Second)

	finalSrc0 := countSrc0.Load()
	finalSrc1 := countSrc1.Load()
	t.Logf("final: src_0=%d, src_1=%d", finalSrc0, finalSrc1)

	if finalSrc0 <= 0 || finalSrc1 <= 0 {
		t.Fatalf("both sources should have received buffers: src_0=%d, src_1=%d", finalSrc0, finalSrc1)
	}

	dumpDot(t, pipeline, "08_before_null")

	// Cleanup: just set to NULL, patchbay handles pad release internally
	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatal("failed to set pipeline to NULL:", err)
	}
}
