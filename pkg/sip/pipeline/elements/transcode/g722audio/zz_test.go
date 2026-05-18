package g722audio

import (
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/testutils"
)

func TestMain(m *testing.M) {
	glib.SetEnv("GST_DEBUG", glib.GetEnv("GST_DEBUG")+",g722-audio:5", true)
	gst.Init(nil)
	Register()
	os.Exit(m.Run())
}

func TestG722Audio_Pipeline(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, err := gst.NewPipeline("test-g722-audio")
	if err != nil {
		t.Fatal("failed to create pipeline:", err)
	}

	audioSrc, err := gst.NewElement("audiotestsrc")
	if err != nil {
		t.Fatal("failed to create audiotestsrc:", err)
	}
	audioSrc.SetProperty("num-buffers", 150)

	capsFilter, err := gst.NewElement("capsfilter")
	if err != nil {
		t.Fatal("failed to create capsfilter:", err)
	}
	capsFilter.SetProperty("caps", gst.NewCapsFromString("audio/x-raw,rate=16000,channels=1,format=S16LE"))

	encoder, err := gst.NewElement("avenc_g722")
	if err != nil {
		t.Fatal("failed to create avenc_g722:", err)
	}

	payloader, err := gst.NewElement("rtpg722pay")
	if err != nil {
		t.Fatal("failed to create rtpg722pay:", err)
	}

	transcoder, err := gst.NewElement("g722-audio")
	if err != nil {
		t.Fatal("failed to create g722-audio:", err)
	}

	sink, err := gst.NewElement("fakesink")
	if err != nil {
		t.Fatal("failed to create fakesink:", err)
	}
	sink.SetProperty("sync", false)

	if err := pipeline.AddMany(audioSrc, capsFilter, encoder, payloader, transcoder, sink); err != nil {
		t.Fatal("failed to add elements to pipeline:", err)
	}

	if err := gst.ElementLinkMany(audioSrc, capsFilter, encoder, payloader, transcoder, sink); err != nil {
		t.Fatal("failed to link elements:", err)
	}

	var bufferCount atomic.Int32
	sinkPad := sink.GetStaticPad("sink")
	if sinkPad == nil {
		t.Fatal("failed to get sink pad from fakesink")
	}
	sinkPad.AddProbe(gst.PadProbeTypeBuffer|gst.PadProbeTypeBufferList, func(self *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		bufferCount.Add(1)
		return gst.PadProbeOK
	})

	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		t.Fatal("failed to set pipeline to PLAYING:", err)
	}

	bus := pipeline.GetPipelineBus()
	timeout := gst.ClockTime(time.Second)
	deadline := time.Now().Add(60 * time.Second)

	for time.Now().Before(deadline) {
		msg := bus.TimedPop(timeout)
		if msg == nil {
			continue
		}

		switch msg.Type() {
		case gst.MessageEOS:
			t.Log("received EOS")
			goto done
		case gst.MessageError:
			gerr := msg.ParseError()
			t.Fatal("pipeline error:", gerr.Error())
		}
	}
	t.Fatal("pipeline timed out waiting for EOS")

done:
	dotData := pipeline.DebugBinToDotData(gst.DebugGraphShowAll)
	if err := os.WriteFile("g722_audio_test.dot", []byte(dotData), 0644); err != nil {
		t.Logf("failed to write DOT file: %v", err)
	}

	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatal("failed to set pipeline to NULL:", err)
	}

	count := bufferCount.Load()
	t.Logf("received %d buffers", count)
	if count <= 0 {
		t.Fatal("no buffers received through g722-audio element")
	}
}
