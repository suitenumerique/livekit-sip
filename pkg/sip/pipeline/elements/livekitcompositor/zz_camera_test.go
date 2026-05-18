package livekitcompositor_test

import (
	"testing"
	"time"

	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/testutils"
)

// All tests in this file are camera-only. None of them create audio sources,
// request audio sink pads, or observe audio state. Camera correctness must be
// provable from the video branch alone.
//
// Each test ends with:
//   1. Explicit ReleaseRequestPad on every ghost sink pad we requested.
//   2. pipeline.SetState(NULL) to stop the dataflow.
//   3. cleanup() on every struct that holds GStreamer wrappers + nil-ing
//      pipeline/compositor locals so the Go GC can finalize them.
//   4. testutils.AssertNoLeaks(t) (deferred at the top) triggers
//      runtime.GC() ×5 + SIGUSR1 to the leak tracer.

// Test 1 — Background produces even without any active-speakers signal.
// The black fallback on compositor sink_0 has alpha=1 by construction, and
// every participant sink pad starts at alpha=0, so the compositor must keep
// emitting black frames as long as it is PLAYING.
func TestCamera_BackgroundProducesWithoutSpeakers(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, compositor := newPipeline(t, "test-camera-bg")
	alice := attachCameraParticipant(t, pipeline, compositor, "alice", 2001, 0)
	sink := newFakeSink(t, pipeline)
	linkCompositorVideoOut(t, compositor, sink.Convert)

	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		t.Fatalf("failed to set PLAYING: %v", err)
	}
	dumpDot(t, pipeline, "01_playing")

	time.Sleep(2 * time.Second)
	dumpDot(t, pipeline, "02_after_wait")

	if n := sink.Count.Load(); n == 0 {
		t.Fatal("no video buffers produced — background fallback should keep emitting even without active-speakers")
	} else {
		t.Logf("received %d video buffers", n)
	}

	alice.release(compositor)
	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatalf("failed to set NULL: %v", err)
	}
	alice.cleanup()
	sink.cleanup()
	compositor = nil
	pipeline = nil
	_, _ = compositor, pipeline
}

// Test 2 — Single speaker: 1 participant, emit active-speakers, verify a
// non-empty MKV is written. Visual check: colored ball fills the full frame
// (1x1 grid).
func TestCamera_SingleSpeaker(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, compositor := newPipeline(t, "test-camera-1speaker")
	alice := attachCameraParticipant(t, pipeline, compositor, "alice", 2001, 0)
	sink := newMKVSink(t, pipeline, "video.mkv")
	linkCompositorVideoOut(t, compositor, sink.Convert)

	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		t.Fatalf("failed to set PLAYING: %v", err)
	}

	// Give the TrackSourceInfo probe time to register the participant before
	// emitting active-speakers — otherwise applyCameraLayout finds no pad.
	time.Sleep(1 * time.Second)

	emitActiveSpeakers(t, compositor, "alice")
	time.Sleep(3 * time.Second)
	dumpDot(t, pipeline, "01_alice_speaking")

	alice.release(compositor)
	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatalf("failed to set NULL: %v", err)
	}

	if n := sink.Count.Load(); n == 0 {
		t.Fatal("no video buffers produced")
	} else {
		t.Logf("received %d video buffers", n)
	}
	assertMKVNonEmpty(t, sink.Path)

	alice.cleanup()
	sink.cleanup()
	compositor = nil
	pipeline = nil
	_, _ = compositor, pipeline
}

// Test 3 — Two speakers side by side (2x1 grid).
func TestCamera_TwoSpeakersGrid(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, compositor := newPipeline(t, "test-camera-2grid")
	alice := attachCameraParticipant(t, pipeline, compositor, "alice", 2001, 0)
	bob := attachCameraParticipant(t, pipeline, compositor, "bob", 2002, 1)
	sink := newMKVSink(t, pipeline, "video.mkv")
	linkCompositorVideoOut(t, compositor, sink.Convert)

	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		t.Fatalf("failed to set PLAYING: %v", err)
	}
	time.Sleep(1 * time.Second)

	emitActiveSpeakers(t, compositor, "alice", "bob")
	time.Sleep(3 * time.Second)
	dumpDot(t, pipeline, "01_2speakers")

	alice.release(compositor)
	bob.release(compositor)
	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatalf("failed to set NULL: %v", err)
	}

	if n := sink.Count.Load(); n == 0 {
		t.Fatal("no video buffers produced")
	} else {
		t.Logf("received %d video buffers", n)
	}
	assertMKVNonEmpty(t, sink.Path)

	alice.cleanup()
	bob.cleanup()
	sink.cleanup()
	compositor = nil
	pipeline = nil
	_, _ = compositor, pipeline
}

// Test 4 — Four speakers in a 2x2 grid. Exercises cameraComputeSize for the
// common full-grid case (rows=cols=2).
func TestCamera_FourSpeakersGrid(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, compositor := newPipeline(t, "test-camera-4grid")
	alice := attachCameraParticipant(t, pipeline, compositor, "alice", 2001, 0)
	bob := attachCameraParticipant(t, pipeline, compositor, "bob", 2002, 1)
	carol := attachCameraParticipant(t, pipeline, compositor, "carol", 2003, 2)
	dave := attachCameraParticipant(t, pipeline, compositor, "dave", 2004, 3)
	sink := newMKVSink(t, pipeline, "video.mkv")
	linkCompositorVideoOut(t, compositor, sink.Convert)

	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		t.Fatalf("failed to set PLAYING: %v", err)
	}
	time.Sleep(1 * time.Second)

	emitActiveSpeakers(t, compositor, "alice", "bob", "carol", "dave")
	time.Sleep(3 * time.Second)
	dumpDot(t, pipeline, "01_4speakers")

	alice.release(compositor)
	bob.release(compositor)
	carol.release(compositor)
	dave.release(compositor)
	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatalf("failed to set NULL: %v", err)
	}

	if n := sink.Count.Load(); n == 0 {
		t.Fatal("no video buffers produced")
	} else {
		t.Logf("received %d video buffers", n)
	}
	assertMKVNonEmpty(t, sink.Path)

	alice.cleanup()
	bob.cleanup()
	carol.cleanup()
	dave.cleanup()
	sink.cleanup()
	compositor = nil
	pipeline = nil
	_, _ = compositor, pipeline
}

// Test 5 — 3 participants rotating through 2 active slots. Verifies that
// swapping speakers doesn't stall the video branch (sticky-slot remap in
// event.go). MKV should visually show the swap happening.
func TestCamera_SpeakerSwap(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, compositor := newPipeline(t, "test-camera-swap")
	alice := attachCameraParticipant(t, pipeline, compositor, "alice", 2001, 0)
	bob := attachCameraParticipant(t, pipeline, compositor, "bob", 2002, 1)
	carol := attachCameraParticipant(t, pipeline, compositor, "carol", 2003, 2)
	sink := newMKVSink(t, pipeline, "video.mkv")
	linkCompositorVideoOut(t, compositor, sink.Convert)

	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		t.Fatalf("failed to set PLAYING: %v", err)
	}
	time.Sleep(1 * time.Second)

	steps := [][]string{
		{"alice", "bob"},
		{"alice", "carol"},
		{"bob", "carol"},
		{"alice", "bob"},
	}
	prev := int32(0)
	for i, speakers := range steps {
		emitActiveSpeakers(t, compositor, speakers...)
		time.Sleep(1500 * time.Millisecond)
		dumpDot(t, pipeline, "step_"+speakers[0]+"_"+speakers[1])
		cur := sink.Count.Load()
		if cur <= prev {
			t.Fatalf("video stalled at step %d (%v): count went from %d to %d", i, speakers, prev, cur)
		}
		t.Logf("step %d (%v): %d video buffers", i, speakers, cur)
		prev = cur
	}

	alice.release(compositor)
	bob.release(compositor)
	carol.release(compositor)
	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatalf("failed to set NULL: %v", err)
	}
	assertMKVNonEmpty(t, sink.Path)

	alice.cleanup()
	bob.cleanup()
	carol.cleanup()
	sink.cleanup()
	compositor = nil
	pipeline = nil
	_, _ = compositor, pipeline
}

// Test 6 — Drop a participant from the active-speakers set; video must keep
// flowing. Exercises hideCameraTrack (alpha → 0) on the dropped participant.
func TestCamera_DropFromLayout(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, compositor := newPipeline(t, "test-camera-drop")
	alice := attachCameraParticipant(t, pipeline, compositor, "alice", 2001, 0)
	bob := attachCameraParticipant(t, pipeline, compositor, "bob", 2002, 1)
	sink := newMKVSink(t, pipeline, "video.mkv")
	linkCompositorVideoOut(t, compositor, sink.Convert)

	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		t.Fatalf("failed to set PLAYING: %v", err)
	}
	time.Sleep(1 * time.Second)

	emitActiveSpeakers(t, compositor, "alice", "bob")
	time.Sleep(2 * time.Second)
	dumpDot(t, pipeline, "01_both")
	before := sink.Count.Load()
	if before == 0 {
		t.Fatal("no video buffers before drop")
	}

	emitActiveSpeakers(t, compositor, "alice")
	time.Sleep(2 * time.Second)
	dumpDot(t, pipeline, "02_only_alice")
	after := sink.Count.Load()
	if after <= before {
		t.Fatalf("video stalled after dropping bob: before=%d after=%d", before, after)
	}
	t.Logf("before drop: %d, after drop: %d", before, after)

	alice.release(compositor)
	bob.release(compositor)
	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatalf("failed to set NULL: %v", err)
	}
	assertMKVNonEmpty(t, sink.Path)

	alice.cleanup()
	bob.cleanup()
	sink.cleanup()
	compositor = nil
	pipeline = nil
	_, _ = compositor, pipeline
}

// Test 7 — Release a participant's camera sink pad mid-test; video must keep
// flowing from background + the remaining participant. Exercises
// releaseCameraSinkPad on a live pipeline.
func TestCamera_ReleasePad(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, compositor := newPipeline(t, "test-camera-release")
	alice := attachCameraParticipant(t, pipeline, compositor, "alice", 2001, 0)
	bob := attachCameraParticipant(t, pipeline, compositor, "bob", 2002, 1)
	sink := newFakeSink(t, pipeline)
	linkCompositorVideoOut(t, compositor, sink.Convert)

	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		t.Fatalf("failed to set PLAYING: %v", err)
	}
	time.Sleep(1 * time.Second)

	emitActiveSpeakers(t, compositor, "alice", "bob")
	time.Sleep(2 * time.Second)
	dumpDot(t, pipeline, "01_both_active")
	before := sink.Count.Load()
	if before == 0 {
		t.Fatal("no video buffers before release")
	}

	// Release alice's pad mid-flight. The test verifies the pipeline doesn't
	// stall and exercises releaseCameraSinkPad's normal (live, target-bound)
	// path.
	alice.release(compositor)
	dumpDot(t, pipeline, "02_after_release")

	time.Sleep(2 * time.Second)
	after := sink.Count.Load()
	if after <= before {
		t.Fatalf("video stalled after releasing alice's camera pad: before=%d after=%d", before, after)
	}
	t.Logf("before release: %d, after release: %d", before, after)

	bob.release(compositor)
	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatalf("failed to set NULL: %v", err)
	}

	alice.cleanup()
	bob.cleanup()
	sink.cleanup()
	compositor = nil
	pipeline = nil
	_, _ = compositor, pipeline
}
