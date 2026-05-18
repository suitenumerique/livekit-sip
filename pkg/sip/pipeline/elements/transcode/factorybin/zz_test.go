package factorybin

import (
	"os"
	"testing"
	"time"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/h264rtppaybin"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/h264video"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/opusaudio"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/videoh264"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/videovp8"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/vp8video"
)

func TestMain(m *testing.M) {
	glib.SetEnv("GST_DEBUG", glib.GetEnv("GST_DEBUG")+",factorybin:5", true)
	gst.Init(nil)
	Register()
	h264rtppaybin.Register()
	h264rtppaybin.Register()
	h264video.Register()
	vp8video.Register()
	videoh264.Register()
	videovp8.Register()
	opusaudio.Register()
	os.Exit(m.Run())
}

func TestFactoryBin_Instantiate(t *testing.T) {
	elem, err := gst.NewElementWithProperties("factorybin", map[string]interface{}{
		"factories": glib.NewStrv([]string{"h264-video", "vp8-video"}),
	})
	if err != nil {
		t.Fatal("failed to create factorybin:", err)
	}
	if elem == nil {
		t.Fatal("factorybin element is nil")
	}
}

func newFactoryBin(t *testing.T, factories []string) *gst.Element {
	t.Helper()
	elem, err := gst.NewElementWithProperties("factorybin", map[string]interface{}{
		"factories": glib.NewStrv(factories),
	})
	if err != nil {
		t.Fatal("failed to create factorybin:", err)
	}
	return elem
}

func TestFactoryBin_QueryCaps_SrcPad(t *testing.T) {
	elem := newFactoryBin(t, []string{"h264-video", "vp8-video"})

	srcPad := elem.GetStaticPad("src")
	if srcPad == nil {
		t.Fatal("failed to get src pad")
	}

	caps := srcPad.QueryCaps(nil)
	if caps == nil {
		t.Fatal("QueryCaps returned nil")
	}
	if caps.IsEmpty() {
		t.Fatal("expected non-empty caps on src pad")
	}

	// Both factories expose video/x-raw on their src pad.
	rawCaps := gst.NewCapsFromString("video/x-raw")
	if !caps.CanIntersect(rawCaps) {
		t.Fatalf("src caps %q do not intersect video/x-raw", caps.String())
	}
}

func TestFactoryBin_QueryCaps_SinkPad(t *testing.T) {
	elem := newFactoryBin(t, []string{"h264-video", "vp8-video"})

	sinkPad := elem.GetStaticPad("sink")
	if sinkPad == nil {
		t.Fatal("failed to get sink pad")
	}

	caps := sinkPad.QueryCaps(nil)
	if caps == nil {
		t.Fatal("QueryCaps returned nil")
	}
	if caps.IsEmpty() {
		t.Fatal("expected non-empty caps on sink pad")
	}

	h264Caps := gst.NewCapsFromString("application/x-rtp, media=(string)video, encoding-name=(string)H264")
	if !caps.CanIntersect(h264Caps) {
		t.Fatalf("sink caps %q do not intersect H264 RTP caps", caps.String())
	}

	vp8Caps := gst.NewCapsFromString("application/x-rtp, media=(string)video, encoding-name=(string)VP8")
	if !caps.CanIntersect(vp8Caps) {
		t.Fatalf("sink caps %q do not intersect VP8 RTP caps", caps.String())
	}
}

func TestFactoryBin_QueryCaps_SinkPad_FilterH264(t *testing.T) {
	elem := newFactoryBin(t, []string{"h264-video", "vp8-video"})

	sinkPad := elem.GetStaticPad("sink")
	if sinkPad == nil {
		t.Fatal("failed to get sink pad")
	}

	filter := gst.NewCapsFromString("application/x-rtp, media=(string)video, encoding-name=(string)H264")
	caps := sinkPad.QueryCaps(filter)
	if caps == nil || caps.IsEmpty() {
		t.Fatal("expected non-empty caps when filtering for H264")
	}

	vp8Caps := gst.NewCapsFromString("application/x-rtp, media=(string)video, encoding-name=(string)VP8")
	if caps.CanIntersect(vp8Caps) {
		t.Fatalf("filtered caps %q should not intersect VP8 caps", caps.String())
	}
	if !caps.CanIntersect(filter) {
		t.Fatalf("filtered caps %q should intersect H264 filter", caps.String())
	}
}

func TestFactoryBin_AcceptCaps_SinkPad(t *testing.T) {
	elem := newFactoryBin(t, []string{"h264-video", "vp8-video"})

	sinkPad := elem.GetStaticPad("sink")
	if sinkPad == nil {
		t.Fatal("failed to get sink pad")
	}

	h264 := gst.NewCapsFromString("application/x-rtp, media=(string)video, clock-rate=(int)90000, encoding-name=(string)H264")
	if !sinkPad.QueryAcceptCaps(h264) {
		t.Fatal("sink pad should accept H264 RTP caps")
	}

	vp8 := gst.NewCapsFromString("application/x-rtp, media=(string)video, clock-rate=(int)90000, encoding-name=(string)VP8")
	if !sinkPad.QueryAcceptCaps(vp8) {
		t.Fatal("sink pad should accept VP8 RTP caps")
	}

	bogus := gst.NewCapsFromString("audio/x-raw")
	if sinkPad.QueryAcceptCaps(bogus) {
		t.Fatal("sink pad should not accept audio/x-raw caps")
	}

	unrelatedRtp := gst.NewCapsFromString("application/x-rtp, media=(string)video, clock-rate=(int)90000, encoding-name=(string)AV1")
	if sinkPad.QueryAcceptCaps(unrelatedRtp) {
		t.Fatal("sink pad should not accept AV1 RTP caps")
	}
}

func TestFactoryBin_AcceptCaps_SrcPad(t *testing.T) {
	elem := newFactoryBin(t, []string{"h264-video", "vp8-video"})

	srcPad := elem.GetStaticPad("src")
	if srcPad == nil {
		t.Fatal("failed to get src pad")
	}

	raw := gst.NewCapsFromString("video/x-raw")
	if !srcPad.QueryAcceptCaps(raw) {
		t.Fatal("src pad should accept video/x-raw caps")
	}

	bogus := gst.NewCapsFromString("audio/x-raw")
	if srcPad.QueryAcceptCaps(bogus) {
		t.Fatal("src pad should not accept audio/x-raw caps")
	}
}

// runCapsEventPipeline builds a pipeline that pushes real RTP packets of the
// given codec into the factorybin, runs it briefly, and returns the name of
// the child factory element selected by the factorybin's caps-event handler.
func runCapsEventPipeline(t *testing.T, codec string) string {
	t.Helper()

	pipelineName := "test-factorybin-caps-" + codec
	pipeline, err := gst.NewPipeline(pipelineName)
	if err != nil {
		t.Fatal("failed to create pipeline:", err)
	}
	t.Cleanup(func() {
		pipeline.SetState(gst.StateNull)
	})

	src, err := gst.NewElement("videotestsrc")
	if err != nil {
		t.Fatal("failed to create videotestsrc:", err)
	}
	src.SetProperty("num-buffers", 5)

	capsFilter, err := gst.NewElement("capsfilter")
	if err != nil {
		t.Fatal("failed to create capsfilter:", err)
	}
	capsFilter.SetProperty("caps", gst.NewCapsFromString("video/x-raw,width=160,height=120,framerate=15/1"))

	var encoder, payloader *gst.Element
	switch codec {
	case "H264":
		encoder, err = gst.NewElement("x264enc")
		if err != nil {
			t.Fatal("failed to create x264enc:", err)
		}
		payloader, err = gst.NewElement("rtph264pay")
		if err != nil {
			t.Fatal("failed to create rtph264pay:", err)
		}
	case "VP8":
		encoder, err = gst.NewElement("vp8enc")
		if err != nil {
			t.Fatal("failed to create vp8enc:", err)
		}
		payloader, err = gst.NewElement("rtpvp8pay")
		if err != nil {
			t.Fatal("failed to create rtpvp8pay:", err)
		}
	default:
		t.Fatalf("unsupported codec %q", codec)
	}

	factorybin := newFactoryBin(t, []string{"h264-video", "vp8-video"})

	convert, err := gst.NewElement("videoconvert")
	if err != nil {
		t.Fatal("failed to create videoconvert:", err)
	}
	sink, err := gst.NewElement("fakesink")
	if err != nil {
		t.Fatal("failed to create fakesink:", err)
	}
	sink.SetProperty("sync", false)

	if err := pipeline.AddMany(src, capsFilter, encoder, payloader, factorybin, convert, sink); err != nil {
		t.Fatal("failed to add elements to pipeline:", err)
	}
	if err := gst.ElementLinkMany(src, capsFilter, encoder, payloader, factorybin, convert, sink); err != nil {
		t.Fatal("failed to link elements:", err)
	}

	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		t.Fatal("failed to set pipeline to PLAYING:", err)
	}

	bus := pipeline.GetPipelineBus()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		msg := bus.TimedPop(gst.ClockTime(500 * time.Millisecond))
		if msg == nil {
			continue
		}
		switch msg.Type() {
		case gst.MessageEOS:
			goto done
		case gst.MessageError:
			gerr := msg.ParseError()
			dotData := pipeline.DebugBinToDotData(gst.DebugGraphShowAll)
			os.WriteFile(pipelineName+"_test.dot", []byte(dotData), 0644)
			t.Fatal("pipeline error:", gerr.Error())
		}
	}
	t.Fatal("pipeline timed out waiting for EOS")

done:
	dotData := pipeline.DebugBinToDotData(gst.DebugGraphShowAll)
	if err := os.WriteFile(pipelineName+"_test.dot", []byte(dotData), 0644); err != nil {
		t.Logf("failed to write DOT file: %v", err)
	}

	bin := gst.ToGstBin(factorybin)
	children, err := bin.GetElements()
	if err != nil {
		t.Fatal("failed to get bin elements:", err)
	}
	if len(children) != 1 {
		t.Fatalf("expected 1 child element in factorybin, got %d", len(children))
	}
	return children[0].GetFactory().GetName()
}

func TestFactoryBin_CapsEvent_SelectsH264(t *testing.T) {
	if name := runCapsEventPipeline(t, "H264"); name != "h264-video" {
		t.Fatalf("expected child factory h264-video, got %s", name)
	}
}

func TestFactoryBin_CapsEvent_SelectsVP8(t *testing.T) {
	if name := runCapsEventPipeline(t, "VP8"); name != "vp8-video" {
		t.Fatalf("expected child factory vp8-video, got %s", name)
	}
}

// runEncodePipeline builds a pipeline that feeds raw video into a factorybin
// configured with the video-h264 / video-vp8 encoder factories. The downstream
// branch is constrained (via a depayloader) to a specific RTP codec, which
// should drive factorybin to pick the matching encoder factory based on its
// downstream peer caps query.
func runEncodePipeline(t *testing.T, codec string) string {
	t.Helper()

	pipelineName := "test-factorybin-encode-" + codec
	pipeline, err := gst.NewPipeline(pipelineName)
	if err != nil {
		t.Fatal("failed to create pipeline:", err)
	}
	t.Cleanup(func() {
		pipeline.SetState(gst.StateNull)
	})

	src, err := gst.NewElement("videotestsrc")
	if err != nil {
		t.Fatal("failed to create videotestsrc:", err)
	}
	src.SetProperty("num-buffers", 5)

	capsFilter, err := gst.NewElement("capsfilter")
	if err != nil {
		t.Fatal("failed to create capsfilter:", err)
	}
	capsFilter.SetProperty("caps", gst.NewCapsFromString("video/x-raw,format=I420,width=160,height=120,framerate=15/1"))

	factorybin := newFactoryBin(t, []string{"video-h264", "video-vp8"})

	var depay *gst.Element
	switch codec {
	case "H264":
		depay, err = gst.NewElement("rtph264depay")
		if err != nil {
			t.Fatal("failed to create rtph264depay:", err)
		}
	case "VP8":
		depay, err = gst.NewElement("rtpvp8depay")
		if err != nil {
			t.Fatal("failed to create rtpvp8depay:", err)
		}
	default:
		t.Fatalf("unsupported codec %q", codec)
	}

	sink, err := gst.NewElement("fakesink")
	if err != nil {
		t.Fatal("failed to create fakesink:", err)
	}
	sink.SetProperty("sync", false)

	if err := pipeline.AddMany(src, capsFilter, factorybin, depay, sink); err != nil {
		t.Fatal("failed to add elements to pipeline:", err)
	}
	if err := gst.ElementLinkMany(src, capsFilter, factorybin, depay, sink); err != nil {
		t.Fatal("failed to link elements:", err)
	}

	capsSeen := make(chan struct{}, 1)
	sinkPad := factorybin.GetStaticPad("sink")
	if sinkPad == nil {
		t.Fatal("failed to get factorybin sink pad")
	}
	sinkPad.AddProbe(gst.PadProbeTypeEventDownstream, func(self *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		ev := info.GetEvent()
		if ev != nil && ev.Type() == gst.EventTypeCaps {
			select {
			case capsSeen <- struct{}{}:
			default:
			}
		}
		return gst.PadProbeOK
	})

	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		t.Fatal("failed to set pipeline to PLAYING:", err)
	}

	select {
	case <-capsSeen:
	case <-time.After(15 * time.Second):
		dotData := pipeline.DebugBinToDotData(gst.DebugGraphShowAll)
		os.WriteFile(pipelineName+"_test.dot", []byte(dotData), 0644)
		t.Fatal("timed out waiting for caps event on factorybin sink")
	}

	dotData := pipeline.DebugBinToDotData(gst.DebugGraphShowAll)
	if err := os.WriteFile(pipelineName+"_test.dot", []byte(dotData), 0644); err != nil {
		t.Logf("failed to write DOT file: %v", err)
	}

	bin := gst.ToGstBin(factorybin)
	var children []*gst.Element
	pollDeadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(pollDeadline) {
		children, err = bin.GetElements()
		if err != nil {
			t.Fatal("failed to get bin elements:", err)
		}
		if len(children) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(children) != 1 {
		t.Fatalf("expected 1 child element in factorybin, got %d", len(children))
	}
	return children[0].GetFactory().GetName()
}

func TestFactoryBin_Encode_SelectsVideoH264(t *testing.T) {
	if name := runEncodePipeline(t, "H264"); name != "video-h264" {
		t.Fatalf("expected child factory video-h264, got %s", name)
	}
}

func TestFactoryBin_Encode_SelectsVideoVP8(t *testing.T) {
	if name := runEncodePipeline(t, "VP8"); name != "video-vp8" {
		t.Fatalf("expected child factory video-vp8, got %s", name)
	}
}

func TestFactoryBin_QueryCaps_SinkPad_FilterVP8(t *testing.T) {
	elem := newFactoryBin(t, []string{"h264-video", "vp8-video"})

	sinkPad := elem.GetStaticPad("sink")
	if sinkPad == nil {
		t.Fatal("failed to get sink pad")
	}

	filter := gst.NewCapsFromString("application/x-rtp, media=(string)video, encoding-name=(string)VP8")
	caps := sinkPad.QueryCaps(filter)
	if caps == nil || caps.IsEmpty() {
		t.Fatal("expected non-empty caps when filtering for VP8")
	}

	h264Caps := gst.NewCapsFromString("application/x-rtp, media=(string)video, encoding-name=(string)H264")
	if caps.CanIntersect(h264Caps) {
		t.Fatalf("filtered caps %q should not intersect H264 caps", caps.String())
	}
	if !caps.CanIntersect(filter) {
		t.Fatalf("filtered caps %q should intersect VP8 filter", caps.String())
	}
}
