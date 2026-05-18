package h264video

// Exported test helpers for h264-video. Must not import "testing" or
// introduce globals/init — otherwise DCE can't strip these symbols
// from production binaries that import h264video.

import (
	"fmt"

	"github.com/go-gst/go-gst/gst"
)

type TestElement struct{}

func Test() TestElement { return TestElement{} }

func (TestElement) Name() string { return "h264-video" }

// BuildSource: videotestsrc -> capsfilter(raw I420 WxH@fps) ->
// x264enc(ultrafast+zerolatency) -> h264parse -> rtph264pay.
// Live source so buffer cadence matches wall-clock frame interval.
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

	enc, err := gst.NewElementWithName("x264enc", "encoder")
	if err != nil {
		return nil, 0, fmt.Errorf("x264enc: %w", err)
	}
	enc.SetProperty("speed-preset", 1)    // ultrafast
	enc.SetProperty("tune", uint(4))      // zerolatency
	enc.SetProperty("key-int-max", uint(12))
	enc.SetProperty("bframes", uint(0))

	parse, err := gst.NewElementWithName("h264parse", "parser")
	if err != nil {
		return nil, 0, fmt.Errorf("h264parse: %w", err)
	}
	parse.SetProperty("config-interval", -1)

	pay, err := gst.NewElementWithName("rtph264pay", "payloader")
	if err != nil {
		return nil, 0, fmt.Errorf("rtph264pay: %w", err)
	}
	pay.SetProperty("mtu", 1200)
	pay.SetProperty("config-interval", 1)

	if err := p.AddMany(src, caps, enc, parse, pay); err != nil {
		return nil, 0, fmt.Errorf("add source chain: %w", err)
	}
	if err := gst.ElementLinkMany(src, caps, enc, parse, pay); err != nil {
		return nil, 0, fmt.Errorf("link source chain: %w", err)
	}
	return pay.GetStaticPad("src"), 0, nil
}

func (TestElement) BuildElement(p *gst.Pipeline, targetWidth, targetHeight int) (*gst.Element, error) {
	e, err := gst.NewElementWithProperties("h264-video", map[string]any{
		"video-width":  uint(targetWidth),
		"video-height": uint(targetHeight),
	})
	if err != nil {
		return nil, fmt.Errorf("h264-video: %w", err)
	}
	if err := p.Add(e); err != nil {
		return nil, fmt.Errorf("add h264-video: %w", err)
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
