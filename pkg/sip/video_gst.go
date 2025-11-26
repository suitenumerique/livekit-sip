package sip

import (
	"fmt"
	"io"

	"errors"

	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/app"
)

func NewGstReader(sink *app.Sink) (*GstReader, error) {
	g := &GstReader{
		sink: sink,
	}

	if err := g.sink.SetState(gst.StatePlaying); err != nil {
		return nil, err
	}

	return g, nil
}

type GstReader struct {
	sink *app.Sink
}

func (g *GstReader) SetState(state gst.State) error {
	return g.sink.SetState(state)
}

func (g *GstReader) Close() error {
	if err := g.sink.SetState(gst.StateNull); err != nil {
		return err
	}
	return nil
}

func (g *GstReader) Read(buf []byte) (int, error) {
	sample := g.sink.PullSample()
	if sample == nil {
		if g.sink.IsEOS() {
			return 0, io.EOF
		}
		return 0, errors.New("failed to pull sample from appsink")
	}
	buffer := sample.GetBuffer()
	if buf == nil {
		return 0, errors.New("failed to get buffer from sample")
	}

	n := copy(buf, buffer.Bytes())
	return n, nil
}

func NewGstWriter(src *app.Source) (*GstWriter, error) {
	g := &GstWriter{
		src: src,
	}

	if err := g.src.SetState(gst.StatePlaying); err != nil {
		return nil, err
	}

	return g, nil
}

type GstWriter struct {
	src *app.Source
}

func (g *GstWriter) String() string {
	return fmt.Sprintf("GstWriteRTP(%s:%s)", g.src.GetFactory().GetName(), g.src.GetName())
}

func (g *GstWriter) Close() error {
	if err := g.src.SetState(gst.StateNull); err != nil {
		return err
	}
	return nil
}

func (g *GstWriter) Write(buf []byte) (int, error) {
	buffer := gst.NewBufferFromBytes(buf)
	if buffer == nil {
		return 0, errors.New("failed to create GST buffer from RTP packet")
	}
	if r := g.src.PushBuffer(buffer); r != gst.FlowOK {
		return 0, errors.New("failed to push buffer to appsrc")
	}
	return len(buf), nil
}

func NewReadWriteRTP(src *app.Source, sink *app.Sink) (*GstReadWriter, error) {
	g := &GstReadWriter{}

	r, err := NewGstReader(sink)
	if err != nil {
		return nil, fmt.Errorf("failed to create GstReadRTP: %w", err)
	}
	g.GstReader = r

	w, err := NewGstWriter(src)
	if err != nil {
		g.GstReader.Close()
		return nil, fmt.Errorf("failed to create GstWriteRTP: %w", err)
	}
	g.GstWriter = w

	return g, nil
}

type GstReadWriter struct {
	*GstReader
	*GstWriter
}

func (g *GstReadWriter) Close() error {
	var errs error
	if err := g.GstWriter.Close(); err != nil {
		errs = errors.Join(errs, fmt.Errorf("failed to close GstWriteRTP: %w", err))
	}
	if err := g.GstReader.Close(); err != nil {
		errs = errors.Join(errs, fmt.Errorf("failed to close GstReadRTP: %w", err))
	}

	return errs
}
