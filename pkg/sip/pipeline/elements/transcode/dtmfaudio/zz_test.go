package dtmfaudio

import (
	"os"
	"testing"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/testutils"
)

func TestMain(m *testing.M) {
	glib.SetEnv("GST_DEBUG", glib.GetEnv("GST_DEBUG")+",dtmf-audio:5", true)
	gst.Init(nil)
	Register()
	os.Exit(m.Run())
}

func TestDtmfAudio_StateChanges(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, err := gst.NewPipeline("test-dtmf-audio-states")
	if err != nil {
		t.Fatal("failed to create pipeline:", err)
	}

	dtmfAudio, err := gst.NewElement("dtmf-audio")
	if err != nil {
		t.Fatal("failed to create dtmf-audio:", err)
	}

	if err := pipeline.Add(dtmfAudio); err != nil {
		t.Fatal("failed to add dtmf-audio to pipeline:", err)
	}

	if err := pipeline.SetState(gst.StateReady); err != nil {
		t.Fatal("failed to set pipeline to READY:", err)
	}

	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatal("failed to set pipeline to NULL:", err)
	}
}
