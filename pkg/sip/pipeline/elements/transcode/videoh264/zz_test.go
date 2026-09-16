package videoh264_test

import (
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/h264rtppaybin"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/rtpcapscodecfilter"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/testutils"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/videoh264"
)

func TestMain(m *testing.M) {
	gst.Init(nil)
	h264rtppaybin.Register()
	rtpcapscodecfilter.Register()
	videoh264.Register()
	os.Exit(m.Run())
}

// TestVideoH264_Smoke runs the element over one burst of 720p30
// synthetic video, checks buffers come out, and verifies no leaks.
// Latency/CPU measurements live in pkg/.../transcode/benchmarks/.
func TestVideoH264_Smoke(t *testing.T) {
	defer testutils.AssertNoLeaks(t)
	encode(t, 1280, 720, 1280, 720)
}

// TestVideoH264_OddSourceSize feeds sources with odd dimensions, as a
// screenshare sent at its native size can have.
func TestVideoH264_OddSourceSize(t *testing.T) {
	defer testutils.AssertNoLeaks(t)
	for _, size := range [][2]int{{1715, 1072}, {919, 540}, {591, 445}} {
		encode(t, size[0], size[1], 1920, 1080)
	}
}

func encode(t *testing.T, width, height, targetWidth, targetHeight int) {
	t.Helper()
	const (
		fps        = 30
		numBuffers = 60
	)

	pipeline, err := gst.NewPipeline("videoh264-test")
	if err != nil {
		t.Fatal("pipeline:", err)
	}

	b := videoh264.Test()
	srcPad, _, err := b.BuildSource(pipeline, width, height, fps, numBuffers)
	if err != nil {
		t.Fatal("BuildSource:", err)
	}
	eut, err := b.BuildElement(pipeline, targetWidth, targetHeight)
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
	var failure string
	for time.Now().Before(deadline) {
		msg := bus.TimedPop(timeout)
		if msg == nil {
			continue
		}
		switch msg.Type() {
		case gst.MessageEOS:
			goto done
		case gst.MessageError:
			failure = msg.ParseError().Error()
			goto done
		}
	}
	failure = "timed out waiting for EOS"

done:
	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatal("SetState NULL:", err)
	}
	if failure != "" {
		t.Fatalf("%dx%d into %dx%d: %s", width, height, targetWidth, targetHeight, failure)
	}
	if got := bufferCount.Load(); got <= 0 {
		t.Fatalf("%dx%d into %dx%d: no buffers received through video-h264", width, height, targetWidth, targetHeight)
	}
}
