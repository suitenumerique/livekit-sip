package videovp9_test

import (
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/testutils"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/videovp9"
)

func TestMain(m *testing.M) {
	// testutils.init() sets GST_TRACERS/GST_DEBUG before gst.Init reads
	// them, triggered automatically by importing testutils below.
	gst.Init(nil)
	videovp9.Register()
	os.Exit(m.Run())
}

// TestVideoVp9_Smoke runs the element over one burst of 720p30 synthetic
// video, checks buffers come out, and verifies no leaks. Latency/CPU
// measurements live in pkg/.../transcode/benchmarks/.
func TestVideoVp9_Smoke(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	const (
		width      = 1280
		height     = 720
		fps        = 30
		numBuffers = 150
	)

	pipeline, err := gst.NewPipeline("videovp9-smoke")
	if err != nil {
		t.Fatal("pipeline:", err)
	}

	b := videovp9.Test()
	srcPad, _, err := b.BuildSource(pipeline, width, height, fps, numBuffers)
	if err != nil {
		t.Fatal("BuildSource:", err)
	}
	eut, err := b.BuildElement(pipeline, width, height)
	if err != nil {
		t.Fatal("BuildElement:", err)
	}
	sinkPad, _, err := b.BuildSink(pipeline)
	if err != nil {
		t.Fatal("BuildSink:", err)
	}
	if ret := srcPad.Link(eut.GetStaticPad("sink")); ret != gst.PadLinkOK {
		t.Fatal("link source -> eut:", ret)
	}
	if ret := eut.GetStaticPad("src").Link(sinkPad); ret != gst.PadLinkOK {
		t.Fatal("link eut -> sink:", ret)
	}

	var bufferCount atomic.Int32
	sinkPad.AddProbe(gst.PadProbeTypeBuffer|gst.PadProbeTypeBufferList, func(_ *gst.Pad, _ *gst.PadProbeInfo) gst.PadProbeReturn {
		bufferCount.Add(1)
		return gst.PadProbeOK
	})

	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		t.Fatal("SetState PLAYING:", err)
	}

	bus := pipeline.GetPipelineBus()
	timeout := gst.ClockTime(time.Second)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		msg := bus.TimedPop(timeout)
		if msg == nil {
			continue
		}
		switch msg.Type() {
		case gst.MessageEOS:
			goto done
		case gst.MessageError:
			gerr := msg.ParseError()
			t.Fatal("pipeline error:", gerr.Error())
		}
	}
	t.Fatal("timed out waiting for EOS")

done:
	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatal("SetState NULL:", err)
	}
	if got := bufferCount.Load(); got <= 0 {
		t.Fatalf("no buffers received through video-vp9; expected > 0, got %d", got)
	}
}
