package videovp9

// Exported test helpers for video-vp9. Must not import "testing" or
// introduce globals/init — otherwise DCE can't strip these symbols
// from production binaries that import videovp9.

import (
	"fmt"

	"github.com/go-gst/go-gst/gst"
)

type TestElement struct{}

func Test() TestElement { return TestElement{} }

func (TestElement) Name() string { return "video-vp9" }

// BuildSource: videotestsrc -> capsfilter(raw I420 WxH@fps). The
// element-under-test is the encoder itself, so the source chain only
// needs to produce raw video — no encoder on this side.
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

	if err := p.AddMany(src, caps); err != nil {
		return nil, 0, fmt.Errorf("add source chain: %w", err)
	}
	if err := gst.ElementLinkMany(src, caps); err != nil {
		return nil, 0, fmt.Errorf("link source chain: %w", err)
	}
	return caps.GetStaticPad("src"), 0, nil
}

func (TestElement) BuildElement(p *gst.Pipeline, targetWidth, targetHeight int) (*gst.Element, error) {
	e, err := gst.NewElementWithProperties("video-vp9", map[string]any{
		"video-width":  uint(targetWidth),
		"video-height": uint(targetHeight),
	})
	if err != nil {
		return nil, fmt.Errorf("video-vp9: %w", err)
	}
	if err := p.Add(e); err != nil {
		return nil, fmt.Errorf("add video-vp9: %w", err)
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
