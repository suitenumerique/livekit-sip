package av1video

// Exported test helpers for av1-video. Must not import "testing" or
// introduce globals/init — otherwise DCE can't strip these symbols
// from production binaries that import av1video.

import (
	"fmt"

	"github.com/go-gst/go-gst/gst"
)

type TestElement struct{}

func Test() TestElement { return TestElement{} }

func (TestElement) Name() string { return "av1-video" }

// BuildSource: videotestsrc -> capsfilter(WxH,fps,I420) -> av1enc ->
// av1parse -> rtpav1pay. Live source so buffer cadence matches
// wall-clock frame interval.
func (TestElement) BuildSource(p *gst.Pipeline, width, height, fps, numBuffers int) (*gst.Pad, int, error) {
	src, err := gst.NewElementWithName("videotestsrc", "src")
	if err != nil {
		return nil, 0, fmt.Errorf("videotestsrc: %w", err)
	}
	src.SetProperty("num-buffers", numBuffers)
	src.SetProperty("is-live", true)

	caps, err := gst.NewElementWithName("capsfilter", "src_caps")
	if err != nil {
		return nil, 0, fmt.Errorf("src capsfilter: %w", err)
	}
	caps.SetProperty("caps", gst.NewCapsFromString(
		fmt.Sprintf("video/x-raw,width=%d,height=%d,framerate=%d/1,format=I420", width, height, fps),
	))

	enc, err := gst.NewElementWithName("av1enc", "encoder")
	if err != nil {
		return nil, 0, fmt.Errorf("av1enc: %w", err)
	}
	enc.SetProperty("cpu-used", 10)
	enc.SetProperty("threads", uint(10))
	enc.SetProperty("usage-profile", 1) // realtime
	enc.SetProperty("row-mt", true)
	enc.SetProperty("tile-columns", uint(3))
	enc.SetProperty("tile-rows", uint(2))
	enc.SetProperty("end-usage", 1) // cbr
	enc.SetProperty("target-bitrate", uint(2000))

	parse, err := gst.NewElementWithName("av1parse", "parser")
	if err != nil {
		return nil, 0, fmt.Errorf("av1parse: %w", err)
	}

	pay, err := gst.NewElementWithName("rtpav1pay", "payloader")
	if err != nil {
		return nil, 0, fmt.Errorf("rtpav1pay: %w", err)
	}
	pay.SetProperty("mtu", 1200)

	if err := p.AddMany(src, caps, enc, parse, pay); err != nil {
		return nil, 0, fmt.Errorf("add source chain: %w", err)
	}
	if err := gst.ElementLinkMany(src, caps, enc, parse, pay); err != nil {
		return nil, 0, fmt.Errorf("link source chain: %w", err)
	}
	return pay.GetStaticPad("src"), 0, nil
}

func (TestElement) BuildElement(p *gst.Pipeline, targetWidth, targetHeight int) (*gst.Element, error) {
	e, err := gst.NewElementWithProperties("av1-video", map[string]any{
		"video-width":  uint(targetWidth),
		"video-height": uint(targetHeight),
	})
	if err != nil {
		return nil, fmt.Errorf("av1-video: %w", err)
	}
	if err := p.Add(e); err != nil {
		return nil, fmt.Errorf("add av1-video: %w", err)
	}
	return e, nil
}

func (TestElement) BuildSink(p *gst.Pipeline) (*gst.Pad, int, error) {
	sink, err := gst.NewElementWithName("fakesink", "sink")
	if err != nil {
		return nil, 0, fmt.Errorf("fakesink: %w", err)
	}
	sink.SetProperty("sync", false)
	sink.SetProperty("async", false)
	if err := p.Add(sink); err != nil {
		return nil, 0, fmt.Errorf("add sink: %w", err)
	}
	return sink.GetStaticPad("sink"), 0, nil
}
