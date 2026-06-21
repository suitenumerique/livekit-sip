package factorybin

import (
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/h264rtppaybin"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/rtpcapscodecfilter"
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
	rtpcapscodecfilter.Register()
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

const (
	h264RTPCaps = "application/x-rtp, media=(string)video, clock-rate=(int)90000, encoding-name=(string)H264, payload=(int)96"
	vp8RTPCaps  = "application/x-rtp, media=(string)video, clock-rate=(int)90000, encoding-name=(string)VP8, payload=(int)97"
	av1RTPCaps  = "application/x-rtp, media=(string)video, clock-rate=(int)90000, encoding-name=(string)AV1, payload=(int)98"
)

// newPlayingFactoryBin builds a pipeline of factorybin followed by the given
// downstream elements, sets it to PLAYING and returns the factorybin. A
// downstream chain is required because factory selection peer-queries the src
// pad. Renegotiation tests then drive the factorybin by sending stream-start
// and caps events directly into its sink ghost pad.
func newPlayingFactoryBin(t *testing.T, factories []string, downstream ...string) *gst.Element {
	t.Helper()

	pipeline, err := gst.NewPipeline("test-" + strings.ReplaceAll(t.Name(), "/", "-"))
	if err != nil {
		t.Fatal("failed to create pipeline:", err)
	}
	t.Cleanup(func() {
		pipeline.SetState(gst.StateNull)
	})

	factorybin := newFactoryBin(t, factories)
	elems := []*gst.Element{factorybin}
	for _, name := range downstream {
		elem, err := gst.NewElement(name)
		if err != nil {
			t.Fatalf("failed to create %s: %v", name, err)
		}
		if name == "fakesink" {
			elem.SetProperty("sync", false)
		}
		elems = append(elems, elem)
	}

	if err := pipeline.AddMany(elems...); err != nil {
		t.Fatal("failed to add elements to pipeline:", err)
	}
	if err := gst.ElementLinkMany(elems...); err != nil {
		t.Fatal("failed to link elements:", err)
	}
	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		t.Fatal("failed to set pipeline to PLAYING:", err)
	}
	return factorybin
}

func sendStreamStart(t *testing.T, pad *gst.Pad) {
	t.Helper()
	if !pad.SendEvent(gst.NewStreamStartEvent(t.Name())) {
		t.Fatal("failed to send stream-start event")
	}
}

func sendCaps(t *testing.T, pad *gst.Pad, capsStr string) bool {
	t.Helper()
	return pad.SendEvent(gst.NewCapsEvent(gst.NewCapsFromString(capsStr)))
}

// soleChild asserts the factorybin holds exactly one child element and returns
// its factory name and element name (the latter identifies the instance).
func soleChild(t *testing.T, factorybin *gst.Element) (factoryName, elemName string) {
	t.Helper()
	children, err := gst.ToGstBin(factorybin).GetElements()
	if err != nil {
		t.Fatal("failed to get bin elements:", err)
	}
	if len(children) != 1 {
		t.Fatalf("expected 1 child element in factorybin, got %d", len(children))
	}
	return children[0].GetFactory().GetName(), children[0].GetName()
}

func selectedFactory(t *testing.T, factorybin *gst.Element) string {
	t.Helper()
	v, err := factorybin.GetProperty("selected-factory")
	if err != nil || v == nil {
		return ""
	}
	s, _ := v.(string)
	return s
}

func TestFactoryBin_Switch_Decode(t *testing.T) {
	cases := []struct {
		name          string
		firstCaps     string
		firstFactory  string
		secondCaps    string
		secondFactory string
	}{
		{"H264ToVP8", h264RTPCaps, "h264-video", vp8RTPCaps, "vp8-video"},
		{"VP8ToH264", vp8RTPCaps, "vp8-video", h264RTPCaps, "h264-video"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			factorybin := newPlayingFactoryBin(t, []string{"h264-video", "vp8-video"}, "videoconvert", "fakesink")
			sinkPad := factorybin.GetStaticPad("sink")
			sendStreamStart(t, sinkPad)

			if !sendCaps(t, sinkPad, tc.firstCaps) {
				t.Fatal("first caps event was rejected")
			}
			firstFactory, firstElem := soleChild(t, factorybin)
			if firstFactory != tc.firstFactory {
				t.Fatalf("expected child factory %s, got %s", tc.firstFactory, firstFactory)
			}

			if !sendCaps(t, sinkPad, tc.secondCaps) {
				t.Fatal("second caps event was rejected")
			}
			secondFactory, secondElem := soleChild(t, factorybin)
			if secondFactory != tc.secondFactory {
				t.Fatalf("expected child factory %s after switch, got %s", tc.secondFactory, secondFactory)
			}
			if secondElem == firstElem {
				t.Fatal("expected a new child element instance after the factory switch")
			}
		})
	}
}

func TestFactoryBin_SelectedFactory_Property(t *testing.T) {
	factorybin := newPlayingFactoryBin(t, []string{"h264-video", "vp8-video"}, "videoconvert", "fakesink")
	sinkPad := factorybin.GetStaticPad("sink")

	if name := selectedFactory(t, factorybin); name != "" {
		t.Fatalf("expected empty selected-factory before negotiation, got %q", name)
	}

	sendStreamStart(t, sinkPad)
	if !sendCaps(t, sinkPad, h264RTPCaps) {
		t.Fatal("H264 caps event was rejected")
	}
	if name := selectedFactory(t, factorybin); name != "h264-video" {
		t.Fatalf("expected selected-factory h264-video, got %q", name)
	}

	if !sendCaps(t, sinkPad, vp8RTPCaps) {
		t.Fatal("VP8 caps event was rejected")
	}
	if name := selectedFactory(t, factorybin); name != "vp8-video" {
		t.Fatalf("expected selected-factory vp8-video after switch, got %q", name)
	}
}

func TestFactoryBin_SameCodec_Renegotiation_KeepsChild(t *testing.T) {
	factorybin := newPlayingFactoryBin(t, []string{"h264-video", "vp8-video"}, "videoconvert", "fakesink")
	sinkPad := factorybin.GetStaticPad("sink")
	sendStreamStart(t, sinkPad)

	if !sendCaps(t, sinkPad, h264RTPCaps) {
		t.Fatal("first H264 caps event was rejected")
	}
	firstFactory, firstElem := soleChild(t, factorybin)
	if firstFactory != "h264-video" {
		t.Fatalf("expected child factory h264-video, got %s", firstFactory)
	}

	renegotiated := "application/x-rtp, media=(string)video, clock-rate=(int)90000, encoding-name=(string)H264, payload=(int)102"
	if !sendCaps(t, sinkPad, renegotiated) {
		t.Fatal("renegotiated H264 caps event was rejected")
	}
	secondFactory, secondElem := soleChild(t, factorybin)
	if secondFactory != "h264-video" {
		t.Fatalf("expected child factory h264-video after renegotiation, got %s", secondFactory)
	}
	if secondElem != firstElem {
		t.Fatalf("expected same-codec renegotiation to keep child %s, got new instance %s", firstElem, secondElem)
	}
}

func TestFactoryBin_Encode_SameCodec_Renegotiation(t *testing.T) {
	factorybin := newPlayingFactoryBin(t, []string{"video-h264", "video-vp8"}, "rtph264depay", "fakesink")
	sinkPad := factorybin.GetStaticPad("sink")
	sendStreamStart(t, sinkPad)

	if !sendCaps(t, sinkPad, "video/x-raw, format=(string)I420, width=(int)160, height=(int)120, framerate=(fraction)15/1") {
		t.Fatal("first raw caps event was rejected")
	}
	firstFactory, firstElem := soleChild(t, factorybin)
	if firstFactory != "video-h264" {
		t.Fatalf("expected child factory video-h264, got %s", firstFactory)
	}

	if !sendCaps(t, sinkPad, "video/x-raw, format=(string)I420, width=(int)320, height=(int)240, framerate=(fraction)15/1") {
		t.Fatal("renegotiated raw caps event was rejected")
	}
	secondFactory, secondElem := soleChild(t, factorybin)
	if secondFactory != "video-h264" {
		t.Fatalf("expected child factory video-h264 after renegotiation, got %s", secondFactory)
	}
	if secondElem != firstElem {
		t.Fatalf("expected resolution change to keep child %s, got new instance %s", firstElem, secondElem)
	}
}

func TestFactoryBin_Switch_NoMatchingFactory_KeepsOldChild(t *testing.T) {
	factorybin := newPlayingFactoryBin(t, []string{"h264-video", "vp8-video"}, "videoconvert", "fakesink")
	sinkPad := factorybin.GetStaticPad("sink")
	sendStreamStart(t, sinkPad)

	if !sendCaps(t, sinkPad, h264RTPCaps) {
		t.Fatal("H264 caps event was rejected")
	}
	_, firstElem := soleChild(t, factorybin)

	if sendCaps(t, sinkPad, av1RTPCaps) {
		t.Fatal("AV1 caps event should have been rejected, no factory supports it")
	}
	factoryName, elemName := soleChild(t, factorybin)
	if factoryName != "h264-video" || elemName != firstElem {
		t.Fatalf("expected old child %s (h264-video) to survive rejected caps, got %s (%s)", firstElem, elemName, factoryName)
	}
}

// TestFactoryBin_Switch_RealData feeds real RTP data through an input-selector
// into the factorybin, then flips the selector from the H264 branch to the VP8
// branch mid-stream. It verifies that the factorybin swaps its child decoder
// and that buffers keep flowing out of it after the switch.
func TestFactoryBin_Switch_RealData(t *testing.T) {
	pipelineName := "test-factorybin-switch-realdata"
	pipeline, err := gst.NewPipeline(pipelineName)
	if err != nil {
		t.Fatal("failed to create pipeline:", err)
	}
	t.Cleanup(func() {
		pipeline.SetState(gst.StateNull)
	})

	dumpDot := func(stage string) {
		dotData := pipeline.DebugBinToDotData(gst.DebugGraphShowAll)
		os.WriteFile(pipelineName+"_"+stage+"_test.dot", []byte(dotData), 0644)
	}

	rawCaps := gst.NewCapsFromString("video/x-raw, width=(int)160, height=(int)120, framerate=(fraction)15/1")

	makeBranch := func(encoderName string, encoderProps map[string]interface{}, payloaderName string) *gst.Element {
		src, err := gst.NewElement("videotestsrc")
		if err != nil {
			t.Fatal("failed to create videotestsrc:", err)
		}
		capsFilter, err := gst.NewElement("capsfilter")
		if err != nil {
			t.Fatal("failed to create capsfilter:", err)
		}
		capsFilter.SetProperty("caps", rawCaps)
		encoder, err := gst.NewElementWithProperties(encoderName, encoderProps)
		if err != nil {
			t.Fatalf("failed to create %s: %v", encoderName, err)
		}
		payloader, err := gst.NewElement(payloaderName)
		if err != nil {
			t.Fatalf("failed to create %s: %v", payloaderName, err)
		}
		if err := pipeline.AddMany(src, capsFilter, encoder, payloader); err != nil {
			t.Fatal("failed to add branch elements to pipeline:", err)
		}
		if err := gst.ElementLinkMany(src, capsFilter, encoder, payloader); err != nil {
			t.Fatal("failed to link branch elements:", err)
		}
		return payloader
	}

	// All-keyframe encoding so the decoder can pick up mid-stream right after
	// the selector switch.
	h264Pay := makeBranch("x264enc", map[string]interface{}{"key-int-max": 1}, "rtph264pay")
	vp8Pay := makeBranch("vp8enc", map[string]interface{}{"deadline": int64(1), "keyframe-max-dist": 1}, "rtpvp8pay")

	selector, err := gst.NewElement("input-selector")
	if err != nil {
		t.Fatal("failed to create input-selector:", err)
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

	if err := pipeline.AddMany(selector, factorybin, convert, sink); err != nil {
		t.Fatal("failed to add elements to pipeline:", err)
	}
	if err := gst.ElementLinkMany(selector, factorybin, convert, sink); err != nil {
		t.Fatal("failed to link elements:", err)
	}

	h264SelPad := selector.GetRequestPad("sink_%u")
	vp8SelPad := selector.GetRequestPad("sink_%u")
	if h264SelPad == nil || vp8SelPad == nil {
		t.Fatal("failed to request input-selector sink pads")
	}
	if ret := h264Pay.GetStaticPad("src").Link(h264SelPad); ret != gst.PadLinkOK {
		t.Fatal("failed to link H264 branch to selector:", ret)
	}
	if ret := vp8Pay.GetStaticPad("src").Link(vp8SelPad); ret != gst.PadLinkOK {
		t.Fatal("failed to link VP8 branch to selector:", ret)
	}
	selector.SetProperty("active-pad", h264SelPad)

	var bufferCount atomic.Int64
	factorybin.GetStaticPad("src").AddProbe(gst.PadProbeTypeBuffer, func(pad *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		bufferCount.Add(1)
		return gst.PadProbeOK
	})

	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		t.Fatal("failed to set pipeline to PLAYING:", err)
	}

	waitFor := func(desc string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			if cond() {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		dumpDot("timeout")
		t.Fatal("timed out waiting for", desc)
	}

	waitFor("decoded H264 buffers", func() bool { return bufferCount.Load() >= 5 })
	factoryName, _ := soleChild(t, factorybin)
	if factoryName != "h264-video" {
		t.Fatalf("expected child factory h264-video before switch, got %s", factoryName)
	}
	dumpDot("before-switch")

	selector.SetProperty("active-pad", vp8SelPad)

	// The swap happens on the streaming thread when the first VP8 caps event
	// arrives, so the bin may transiently hold two children; poll on the
	// selected-factory property instead of asserting a single child.
	waitFor("factorybin to switch to vp8-video", func() bool {
		return selectedFactory(t, factorybin) == "vp8-video"
	})
	countAtSwitch := bufferCount.Load()
	waitFor("decoded VP8 buffers after switch", func() bool {
		return bufferCount.Load() >= countAtSwitch+5
	})

	factoryName, _ = soleChild(t, factorybin)
	if factoryName != "vp8-video" {
		t.Fatalf("expected child factory vp8-video after switch, got %s", factoryName)
	}
	dumpDot("after-switch")
}

// TestFactoryBin_Encode_DownstreamCapsChange covers renegotiation driven from
// the src side: downstream first constrains factorybin to H264 RTP, then
// changes its constraint to VP8 RTP. Upstream reacts to the resulting
// RECONFIGURE event by re-announcing its (unchanged) raw caps, and factorybin
// must notice that the current child can no longer be linked downstream and
// switch to the VP8 encoder.
func TestFactoryBin_Encode_DownstreamCapsChange(t *testing.T) {
	pipeline, err := gst.NewPipeline("test-" + t.Name())
	if err != nil {
		t.Fatal("failed to create pipeline:", err)
	}
	t.Cleanup(func() {
		pipeline.SetState(gst.StateNull)
	})

	factorybin := newFactoryBin(t, []string{"video-h264", "video-vp8"})
	outFilter, err := gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString(h264RTPCaps),
	})
	if err != nil {
		t.Fatal("failed to create capsfilter:", err)
	}
	sink, err := gst.NewElement("fakesink")
	if err != nil {
		t.Fatal("failed to create fakesink:", err)
	}
	sink.SetProperty("sync", false)

	if err := pipeline.AddMany(factorybin, outFilter, sink); err != nil {
		t.Fatal("failed to add elements to pipeline:", err)
	}
	if err := gst.ElementLinkMany(factorybin, outFilter, sink); err != nil {
		t.Fatal("failed to link elements:", err)
	}
	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		t.Fatal("failed to set pipeline to PLAYING:", err)
	}

	sinkPad := factorybin.GetStaticPad("sink")
	sendStreamStart(t, sinkPad)

	rawCaps := "video/x-raw, format=(string)I420, width=(int)160, height=(int)120, framerate=(fraction)15/1"
	if !sendCaps(t, sinkPad, rawCaps) {
		t.Fatal("raw caps event was rejected")
	}
	factoryName, _ := soleChild(t, factorybin)
	if factoryName != "video-h264" {
		t.Fatalf("expected child factory video-h264, got %s", factoryName)
	}

	// Downstream changes its constraint; the capsfilter sends RECONFIGURE
	// upstream. Upstream re-announces the same raw caps in response.
	outFilter.SetProperty("caps", gst.NewCapsFromString(vp8RTPCaps))
	if !sendCaps(t, sinkPad, rawCaps) {
		t.Fatal("raw caps event after downstream change was rejected")
	}
	factoryName, _ = soleChild(t, factorybin)
	if factoryName != "video-vp8" {
		t.Fatalf("expected child factory video-vp8 after downstream caps change, got %s", factoryName)
	}
}

// TestFactoryBin_Encode_DownstreamCapsChange_RealData is the data-flow variant:
// a live encode pipeline where only the downstream capsfilter changes from H264
// RTP to VP8 RTP mid-stream. factorybin must swap encoders and keep buffers
// flowing without the application re-sending any sink caps.
func TestFactoryBin_Encode_DownstreamCapsChange_RealData(t *testing.T) {
	pipelineName := "test-factorybin-downstream-capschange"
	pipeline, err := gst.NewPipeline(pipelineName)
	if err != nil {
		t.Fatal("failed to create pipeline:", err)
	}
	t.Cleanup(func() {
		pipeline.SetState(gst.StateNull)
	})

	dumpDot := func(stage string) {
		dotData := pipeline.DebugBinToDotData(gst.DebugGraphShowAll)
		os.WriteFile(pipelineName+"_"+stage+"_test.dot", []byte(dotData), 0644)
	}

	src, err := gst.NewElement("videotestsrc")
	if err != nil {
		t.Fatal("failed to create videotestsrc:", err)
	}
	inFilter, err := gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString("video/x-raw, format=(string)I420, width=(int)160, height=(int)120, framerate=(fraction)15/1"),
	})
	if err != nil {
		t.Fatal("failed to create input capsfilter:", err)
	}
	factorybin := newFactoryBin(t, []string{"video-h264", "video-vp8"})
	outFilter, err := gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString(h264RTPCaps),
	})
	if err != nil {
		t.Fatal("failed to create output capsfilter:", err)
	}
	sink, err := gst.NewElement("fakesink")
	if err != nil {
		t.Fatal("failed to create fakesink:", err)
	}
	sink.SetProperty("sync", false)

	if err := pipeline.AddMany(src, inFilter, factorybin, outFilter, sink); err != nil {
		t.Fatal("failed to add elements to pipeline:", err)
	}
	if err := gst.ElementLinkMany(src, inFilter, factorybin, outFilter, sink); err != nil {
		t.Fatal("failed to link elements:", err)
	}

	// rtpvp8pay pushes buffer lists rather than single buffers, so the probe
	// must match both.
	var bufferCount atomic.Int64
	factorybin.GetStaticPad("src").AddProbe(gst.PadProbeTypeBuffer|gst.PadProbeTypeBufferList, func(pad *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		bufferCount.Add(1)
		return gst.PadProbeOK
	})

	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		t.Fatal("failed to set pipeline to PLAYING:", err)
	}

	// Drains the bus while waiting so a not-negotiated error fails fast with
	// its message instead of a bare timeout.
	bus := pipeline.GetPipelineBus()
	waitFor := func(desc string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			if cond() {
				return
			}
			if msg := bus.TimedPop(gst.ClockTime(20 * time.Millisecond)); msg != nil && msg.Type() == gst.MessageError {
				dumpDot("error")
				time.Sleep(1 * time.Second)
				t.Fatalf("pipeline error while waiting for %s: %s", desc, msg.ParseError().Error())
			}
		}
		dumpDot("timeout")
		t.Fatal("timed out waiting for", desc)
	}

	waitFor("H264 RTP buffers", func() bool { return bufferCount.Load() >= 5 })
	factoryName, _ := soleChild(t, factorybin)
	if factoryName != "video-h264" {
		t.Fatalf("expected child factory video-h264 before downstream change, got %s", factoryName)
	}
	dumpDot("before-switch")

	outFilter.SetProperty("caps", gst.NewCapsFromString(vp8RTPCaps))

	waitFor("factorybin to switch to video-vp8", func() bool {
		return selectedFactory(t, factorybin) == "video-vp8"
	})
	countAtSwitch := bufferCount.Load()
	waitFor("VP8 RTP buffers after switch", func() bool {
		return bufferCount.Load() >= countAtSwitch+5
	})
	dumpDot("after-switch")
}

// TestFactoryBin_Encode_DownstreamCapsChange_InFlightBufferSurvives pins the
// race between a reconfigure-driven factory switch and a buffer in flight
// inside the old child: each buffer is held inside the old child for 100ms by
// a sleeping probe — an order of magnitude longer than the swap takes — and
// the downstream change is timed to land while one is held. The swap must
// retarget immediately and defer the old child's teardown until the held
// buffer has drained (IDLE probe on the sink ghost pad's internal proxy).
// Tearing down too early instead flushes the held buffer back to
// videotestsrc, whose task pauses silently on FLUSHING: the switch completes
// but no data ever flows again, with no error on the bus — which is exactly
// what this test would report as "timed out waiting for buffers after the
// switch".
func TestFactoryBin_Encode_DownstreamCapsChange_InFlightBufferSurvives(t *testing.T) {
	pipelineName := "test-factorybin-downstream-inflight-stall"
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
	inFilter, err := gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString("video/x-raw, format=(string)I420, width=(int)160, height=(int)120, framerate=(fraction)15/1"),
	})
	if err != nil {
		t.Fatal("failed to create input capsfilter:", err)
	}
	factorybin := newFactoryBin(t, []string{"video-h264", "video-vp8"})
	outFilter, err := gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString(h264RTPCaps),
	})
	if err != nil {
		t.Fatal("failed to create output capsfilter:", err)
	}
	sink, err := gst.NewElement("fakesink")
	if err != nil {
		t.Fatal("failed to create fakesink:", err)
	}
	sink.SetProperty("sync", false)

	if err := pipeline.AddMany(src, inFilter, factorybin, outFilter, sink); err != nil {
		t.Fatal("failed to add elements to pipeline:", err)
	}
	if err := gst.ElementLinkMany(src, inFilter, factorybin, outFilter, sink); err != nil {
		t.Fatal("failed to link elements:", err)
	}

	var bufferCount atomic.Int64
	factorybin.GetStaticPad("src").AddProbe(gst.PadProbeTypeBuffer|gst.PadProbeTypeBufferList, func(pad *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		bufferCount.Add(1)
		return gst.PadProbeOK
	})

	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		t.Fatal("failed to set pipeline to PLAYING:", err)
	}

	bus := pipeline.GetPipelineBus()
	waitFor := func(desc string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			if cond() {
				return
			}
			if msg := bus.TimedPop(gst.ClockTime(20 * time.Millisecond)); msg != nil && msg.Type() == gst.MessageError {
				t.Fatalf("pipeline error while waiting for %s: %s", desc, msg.ParseError().Error())
			}
		}
		t.Fatal("timed out waiting for", desc)
	}

	waitFor("H264 RTP buffers", func() bool { return bufferCount.Load() >= 3 })
	children, err := gst.ToGstBin(factorybin).GetElements()
	if err != nil || len(children) != 1 {
		t.Fatalf("expected 1 child element in factorybin, got %d (err: %v)", len(children), err)
	}

	// Hold every buffer inside the old child for 100ms — an order of magnitude
	// longer than building the new child takes — so a buffer is guaranteed to
	// be in flight when the teardown starts. inSleep tracks whether a buffer
	// is currently being held, so the downstream change below can be timed to
	// make the collision deterministic.
	var inSleep atomic.Bool
	children[0].GetStaticPad("sink").AddProbe(gst.PadProbeTypeBuffer, func(pad *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		inSleep.Store(true)
		time.Sleep(100 * time.Millisecond)
		inSleep.Store(false)
		return gst.PadProbeOK
	})

	waitFor("a buffer held inside the old child", func() bool { return inSleep.Load() })
	outFilter.SetProperty("caps", gst.NewCapsFromString(vp8RTPCaps))

	waitFor("factorybin to switch to video-vp8", func() bool {
		return selectedFactory(t, factorybin) == "video-vp8"
	})

	// The held buffer is discarded with the old child, but the source must
	// keep running and subsequent buffers must flow through the new child.
	countAtSwitch := bufferCount.Load()
	waitFor("VP8 RTP buffers after the switch", func() bool {
		return bufferCount.Load() >= countAtSwitch+5
	})
}
