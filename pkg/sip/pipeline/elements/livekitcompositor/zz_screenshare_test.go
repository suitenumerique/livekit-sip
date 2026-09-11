package livekitcompositor_test

import (
	"fmt"
	"runtime"
	"testing"
	"time"

	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/livekitbin/livekittracks"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/testutils"
)

// attachScreensharePresenter mirrors attachCameraParticipant for the
// screenshare session (TrackSource_SCREEN_SHARE = 3): a live videotestsrc
// linked to a `sink_3_<ssrc>_<pt>` ghost pad of the compositor.
func attachScreensharePresenter(t *testing.T, pipeline *gst.Pipeline, compositor *gst.Element, sid string, ssrc uint, colorIdx int) *cameraParticipant {
	t.Helper()
	const pt uint = 97
	colors := videotestsrcColors[colorIdx%len(videotestsrcColors)]

	src, err := gst.NewElementWithProperties("videotestsrc", map[string]any{
		"is-live":          true,
		"pattern":          18,
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
		t.Fatalf("failed to add screenshare source elements: %v", err)
	}
	if err := src.Link(caps); err != nil {
		t.Fatalf("failed to link screenshare source chain: %v", err)
	}

	sinkName := fmt.Sprintf("sink_%d_%d_%d", livekit.TrackSource_SCREEN_SHARE, ssrc, pt)
	sinkPad := compositor.GetRequestPad(sinkName)
	if sinkPad == nil {
		t.Fatalf("failed to request screenshare sink pad %s", sinkName)
	}
	capsSrc := caps.GetStaticPad("src")
	if ret := capsSrc.Link(sinkPad); ret != gst.PadLinkOK {
		t.Fatalf("failed to link screenshare source to %s: %v", sinkName, ret)
	}
	// Presenters are attached while the pipeline plays: a live source that
	// starts before it is linked stops with not-linked and never delivers.
	for _, e := range []*gst.Element{src, caps} {
		if !e.SyncStateWithParent() {
			t.Logf("warning: failed to sync %s with parent", e.GetName())
		}
	}

	info := livekittracks.TrackSourceInfo{
		ParticipantSID:  sid,
		ParticipantName: sid,
		TrackSID:        sid + "-share",
		Source:          livekit.TrackSource_SCREEN_SHARE,
		Kind:            "video",
		MimeType:        "video/x-raw",
		SSRC:            ssrc,
		PT:              pt,
	}
	capsSrc.AddProbe(gst.PadProbeTypeBuffer|gst.PadProbeTypeBufferList, func(p *gst.Pad, _ *gst.PadProbeInfo) gst.PadProbeReturn {
		structure := info.Structure()
		runtime.SetFinalizer(structure, nil)
		p.PushEvent(gst.NewCustomEvent(gst.EventTypeCustomDownstreamSticky, structure))
		return gst.PadProbeRemove
	})
	return &cameraParticipant{Src: src, Caps: caps, SinkPad: sinkPad}
}

func screenshareSrcPad(compositor *gst.Element) *gst.Pad {
	return compositor.GetStaticPad(fmt.Sprintf("src_%d", livekit.TrackSource_SCREEN_SHARE))
}

func linkCompositorScreenshareOut(t *testing.T, compositor *gst.Element, sinkHead *gst.Element) {
	t.Helper()
	srcPad := screenshareSrcPad(compositor)
	if srcPad == nil {
		t.Fatal("compositor src_3 pad not found — was a screenshare sink pad requested first?")
	}
	if ret := srcPad.Link(sinkHead.GetStaticPad("sink")); ret != gst.PadLinkOK {
		t.Fatalf("failed to link compositor src_3 to sink: %v", ret)
	}
}

// A presenter switch (B starts, then A is released) must keep the screenshare
// output pad and the frames flowing: no teardown, no BFCP floor churn.
func TestScreenshare_PresenterSwitchKeepsSrcPad(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, compositor := newPipeline(t, "test-screenshare-switch")
	alice := attachScreensharePresenter(t, pipeline, compositor, "alice", 3001, 0)
	sink := newFakeSink(t, pipeline)
	linkCompositorScreenshareOut(t, compositor, sink.Convert)

	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		t.Fatalf("failed to set PLAYING: %v", err)
	}
	time.Sleep(1500 * time.Millisecond)
	if sink.Count.Load() == 0 {
		t.Fatal("no screenshare frames from the first presenter")
	}

	bob := attachScreensharePresenter(t, pipeline, compositor, "bob", 3002, 1)
	time.Sleep(300 * time.Millisecond)
	alice.release(compositor)
	before := sink.Count.Load()
	time.Sleep(1500 * time.Millisecond)

	if screenshareSrcPad(compositor) == nil {
		t.Fatal("src_3 was removed during a presenter switch")
	}
	if produced := sink.Count.Load() - before; produced == 0 {
		t.Fatal("no screenshare frames after the presenter switch")
	} else {
		t.Logf("%d frames after the switch", produced)
	}

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

// The reverse interleaving: A is released before B's pad exists. The output
// chain must survive the gap (grace period) and serve B without a rebuild.
func TestScreenshare_GapBetweenPresentersKeepsSrcPad(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, compositor := newPipeline(t, "test-screenshare-gap")
	alice := attachScreensharePresenter(t, pipeline, compositor, "alice", 3011, 0)
	sink := newFakeSink(t, pipeline)
	linkCompositorScreenshareOut(t, compositor, sink.Convert)

	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		t.Fatalf("failed to set PLAYING: %v", err)
	}
	time.Sleep(1500 * time.Millisecond)

	alice.release(compositor)
	time.Sleep(1500 * time.Millisecond) // an app-driven switch: shorter than the grace period
	if screenshareSrcPad(compositor) == nil {
		t.Fatal("src_3 was removed right after the last presenter left")
	}
	bob := attachScreensharePresenter(t, pipeline, compositor, "bob", 3012, 1)
	before := sink.Count.Load()
	time.Sleep(2 * time.Second)
	if screenshareSrcPad(compositor) == nil {
		t.Fatal("src_3 was removed although a new presenter arrived within the grace period")
	}
	if produced := sink.Count.Load() - before; produced == 0 {
		t.Fatal("no screenshare frames from the second presenter")
	}

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
