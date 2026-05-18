package livekitcompositor_test

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/livekitbin/livekittracks"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/livekitcompositor"

	// Imported so testutils' init() configures GST_TRACERS=leaks and
	// GST_LEAKS_TRACER_SIG=SIGUSR1 before gst.Init is called in TestMain.
	_ "github.com/livekit/sip/pkg/sip/pipeline/elements/testutils"
)

func TestMain(m *testing.M) {
	glib.SetEnv("GST_DEBUG", glib.GetEnv("GST_DEBUG")+",livekit_compositor:5", true)
	gst.Init(nil)
	livekitcompositor.Register()
	os.Exit(m.Run())
}

// videotestsrcColors maps color indices to distinct foreground/background ARGB
// pairs so each participant shows up as a different colored ball in the MKV.
var videotestsrcColors = [][2]uint32{
	{0xFFFFFFFF, 0xFF000000}, // white on black
	{0xFFFF0000, 0xFF0000FF}, // red on blue
	{0xFF00FF00, 0xFFFF00FF}, // green on magenta
	{0xFFFFFF00, 0xFF800080}, // yellow on purple
	{0xFF00FFFF, 0xFF804000}, // cyan on brown
}

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
		t.Logf("warning: failed to write dot file %s: %v", path, err)
	}
}

func newPipeline(t *testing.T, name string) (*gst.Pipeline, *gst.Element) {
	t.Helper()
	pipeline, err := gst.NewPipeline(name)
	if err != nil {
		t.Fatalf("failed to create pipeline: %v", err)
	}
	compositor, err := gst.NewElement("livekit_compositor")
	if err != nil {
		t.Fatalf("failed to create livekit_compositor: %v", err)
	}
	if err := pipeline.Add(compositor); err != nil {
		t.Fatalf("failed to add livekit_compositor: %v", err)
	}
	if err := pipeline.SetState(gst.StateReady); err != nil {
		t.Fatalf("failed to set pipeline to READY: %v", err)
	}
	return pipeline, compositor
}

func addBufferProbe(t *testing.T, padOwner *gst.Element) *atomic.Int32 {
	t.Helper()
	var count atomic.Int32
	sinkPad := padOwner.GetStaticPad("sink")
	if sinkPad == nil {
		t.Fatalf("failed to get sink pad on %s for buffer probe", padOwner.GetName())
	}
	sinkPad.AddProbe(gst.PadProbeTypeBuffer|gst.PadProbeTypeBufferList, func(_ *gst.Pad, _ *gst.PadProbeInfo) gst.PadProbeReturn {
		count.Add(1)
		return gst.PadProbeOK
	})
	return &count
}

// mkvSink owns the videoconvert → x264enc → matroskamux → filesink chain.
// All fields are exposed so the test can nil them for the leak check.
type mkvSink struct {
	Convert *gst.Element // head — link compositor src to Convert.sink
	Encoder *gst.Element
	Mux     *gst.Element
	Sink    *gst.Element
	Count   *atomic.Int32
	Path    string
}

// cleanup nils every GStreamer wrapper held by the sink so the Go GC can
// finalize them during AssertNoLeaks.
func (s *mkvSink) cleanup() {
	s.Convert = nil
	s.Encoder = nil
	s.Mux = nil
	s.Sink = nil
}

func newMKVSink(t *testing.T, pipeline *gst.Pipeline, filename string) *mkvSink {
	t.Helper()
	dir := testDebugDir(t)
	path, err := filepath.Abs(filepath.Join(dir, filename))
	if err != nil {
		t.Fatalf("failed to resolve mkv path: %v", err)
	}

	convert, err := gst.NewElement("videoconvert")
	if err != nil {
		t.Fatalf("failed to create videoconvert: %v", err)
	}
	encoder, err := gst.NewElementWithProperties("x264enc", map[string]any{
		"tune": 0x4, // zerolatency
	})
	if err != nil {
		t.Fatalf("failed to create x264enc: %v", err)
	}
	mux, err := gst.NewElement("matroskamux")
	if err != nil {
		t.Fatalf("failed to create matroskamux: %v", err)
	}
	sink, err := gst.NewElementWithProperties("filesink", map[string]any{
		"location": path,
	})
	if err != nil {
		t.Fatalf("failed to create filesink: %v", err)
	}

	elements := []*gst.Element{convert, encoder, mux, sink}
	if err := pipeline.AddMany(elements...); err != nil {
		t.Fatalf("failed to add mkv sink elements: %v", err)
	}
	if err := gst.ElementLinkMany(elements...); err != nil {
		t.Fatalf("failed to link mkv sink chain: %v", err)
	}
	for _, e := range elements {
		if !e.SyncStateWithParent() {
			t.Logf("warning: failed to sync %s with parent", e.GetName())
		}
	}

	return &mkvSink{
		Convert: convert,
		Encoder: encoder,
		Mux:     mux,
		Sink:    sink,
		Count:   addBufferProbe(t, convert),
		Path:    path,
	}
}

// fakeSink owns a videoconvert → fakesink chain for tests that only need
// buffer flow (no MKV artifact).
type fakeSink struct {
	Convert *gst.Element // head
	Sink    *gst.Element
	Count   *atomic.Int32
}

func (s *fakeSink) cleanup() {
	s.Convert = nil
	s.Sink = nil
}

func newFakeSink(t *testing.T, pipeline *gst.Pipeline) *fakeSink {
	t.Helper()
	convert, err := gst.NewElement("videoconvert")
	if err != nil {
		t.Fatalf("failed to create videoconvert: %v", err)
	}
	sink, err := gst.NewElementWithProperties("fakesink", map[string]any{"sync": false})
	if err != nil {
		t.Fatalf("failed to create fakesink: %v", err)
	}
	if err := pipeline.AddMany(convert, sink); err != nil {
		t.Fatalf("failed to add fakesink elements: %v", err)
	}
	if err := convert.Link(sink); err != nil {
		t.Fatalf("failed to link fakesink chain: %v", err)
	}
	for _, e := range []*gst.Element{convert, sink} {
		if !e.SyncStateWithParent() {
			t.Logf("warning: failed to sync %s with parent", e.GetName())
		}
	}
	return &fakeSink{
		Convert: convert,
		Sink:    sink,
		Count:   addBufferProbe(t, convert),
	}
}

// linkCompositorVideoOut connects compositor's src_1 (camera source pad,
// created synchronously inside initCamera when the first camera sink is
// requested) to the given sink head.
func linkCompositorVideoOut(t *testing.T, compositor *gst.Element, sinkHead *gst.Element) {
	t.Helper()
	name := fmt.Sprintf("src_%d", livekit.TrackSource_CAMERA)
	srcPad := compositor.GetStaticPad(name)
	if srcPad == nil {
		t.Fatalf("compositor %s pad not found — was a camera sink pad requested first?", name)
	}
	if ret := srcPad.Link(sinkHead.GetStaticPad("sink")); ret != gst.PadLinkOK {
		t.Fatalf("failed to link compositor %s to sink: %v", name, ret)
	}
}

// assertMKVNonEmpty verifies the file on disk is non-zero bytes. Call after
// SetState(NULL) so matroskamux has flushed. Buffer counters alone don't catch
// a broken encoder leaving behind an empty mux output.
func assertMKVNonEmpty(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("mkv file missing at %s: %v", path, err)
	}
	if info.Size() == 0 {
		t.Fatalf("mkv file is empty (0 bytes) at %s — encoder/mux produced no output", path)
	}
	t.Logf("mkv at %s: %d bytes", path, info.Size())
}

// cameraParticipant owns a single participant's video source chain and their
// ghost sink pad on the compositor.
type cameraParticipant struct {
	Src     *gst.Element // videotestsrc
	Caps    *gst.Element // capsfilter
	SinkPad *gst.Pad     // ghost pad on livekit_compositor
}

// release asks the compositor to release this participant's ghost sink pad,
// then nils the stored reference. Call this before setting the pipeline to
// NULL so `gpad.GetTarget()` is still valid in releaseCameraSinkPad.
func (p *cameraParticipant) release(compositor *gst.Element) {
	if p.SinkPad != nil {
		compositor.ReleaseRequestPad(p.SinkPad)
		p.SinkPad = nil
	}
}

// cleanup nils every wrapper held by the participant so the Go GC can finalize
// them during AssertNoLeaks. Safe to call after release().
func (p *cameraParticipant) cleanup() {
	p.Src = nil
	p.Caps = nil
	p.SinkPad = nil
}

// attachCameraParticipant creates a camera-only source for `sid`, requests a
// camera sink pad on the compositor (session=1 = TrackSource_CAMERA), links
// them, and injects the TrackSourceInfo event so the compositor registers the
// participant. This helper never touches audio. Camera tests MUST NOT rely on
// audio state.
func attachCameraParticipant(t *testing.T, pipeline *gst.Pipeline, compositor *gst.Element, sid string, ssrc uint, colorIdx int) *cameraParticipant {
	t.Helper()
	const pt uint = 96
	colors := videotestsrcColors[colorIdx%len(videotestsrcColors)]

	src, err := gst.NewElementWithProperties("videotestsrc", map[string]any{
		"is-live":          true,
		"pattern":          18, // ball
		"foreground-color": colors[0],
		"background-color": colors[1],
	})
	if err != nil {
		t.Fatalf("failed to create videotestsrc: %v", err)
	}
	caps, err := gst.NewElement("capsfilter")
	if err != nil {
		t.Fatalf("failed to create capsfilter: %v", err)
	}
	caps.SetProperty("caps", gst.NewCapsFromString("video/x-raw,format=I420,width=320,height=240,framerate=15/1"))

	if err := pipeline.AddMany(src, caps); err != nil {
		t.Fatalf("failed to add camera source elements: %v", err)
	}
	if err := src.Link(caps); err != nil {
		t.Fatalf("failed to link camera source chain: %v", err)
	}
	for _, e := range []*gst.Element{src, caps} {
		if !e.SyncStateWithParent() {
			t.Logf("warning: failed to sync %s with parent", e.GetName())
		}
	}

	sinkName := fmt.Sprintf("sink_%d_%d_%d", livekit.TrackSource_CAMERA, ssrc, pt)
	sinkPad := compositor.GetRequestPad(sinkName)
	if sinkPad == nil {
		t.Fatalf("failed to request camera sink pad %s", sinkName)
	}

	capsSrc := caps.GetStaticPad("src")
	if ret := capsSrc.Link(sinkPad); ret != gst.PadLinkOK {
		t.Fatalf("failed to link camera source to %s: %v", sinkName, ret)
	}

	info := livekittracks.TrackSourceInfo{
		ParticipantSID:  sid,
		ParticipantName: sid,
		TrackSID:        sid + "-video",
		Source:          livekit.TrackSource_CAMERA,
		Kind:            "video",
		MimeType:        "video/x-raw",
		SSRC:            ssrc,
		PT:              pt,
	}
	capsSrc.AddProbe(gst.PadProbeTypeBuffer|gst.PadProbeTypeBufferList, func(p *gst.Pad, _ *gst.PadProbeInfo) gst.PadProbeReturn {
		structure := info.Structure()
		runtime.SetFinalizer(structure, nil)
		event := gst.NewCustomEvent(gst.EventTypeCustomDownstreamSticky, structure)
		p.PushEvent(event)
		return gst.PadProbeRemove
	})

	return &cameraParticipant{
		Src:     src,
		Caps:    caps,
		SinkPad: sinkPad,
	}
}

// emitActiveSpeakers synthesizes the active-speakers-changed signal the real
// LiveKit bin would emit. Runs the Emit on a goroutine with a 5s watchdog so
// a signal-handler deadlock fails the test instead of hanging.
func emitActiveSpeakers(t *testing.T, compositor *gst.Element, sids ...string) {
	t.Helper()
	levels := make([]float32, len(sids))
	for i := range levels {
		levels[i] = 0.5
	}
	info := livekittracks.ActiveSpeakerChangeInfo{
		ParticipantsSID:   sids,
		AudioLevels:       levels,
		ParticipantTracks: make(map[string][]string),
	}
	structure := info.Structure()
	runtime.SetFinalizer(structure, nil)

	done := make(chan struct{})
	go func() {
		compositor.Emit("active-speakers-changed", structure)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("emitActiveSpeakers deadlocked — Emit did not return within 5s")
	}
}
