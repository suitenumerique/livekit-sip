package sinkwriter

import (
	"fmt"
	"io"
	"math"
	"runtime/cgo"
	"sync/atomic"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/base"
)

var CAT = gst.NewDebugCategory(
	"sinkwriter",
	gst.DebugColorFgYellow,
	"sinkwriter Element",
)

var properties = []*glib.ParamSpec{
	glib.NewUint64Param(
		"handle",
		"Handle",
		"cgo.Handle (uintptr) to a Go object",
		0,
		math.MaxUint64,
		0,
		glib.ParameterWritable,
	),
	glib.NewBoxedParam(
		"caps",
		"Caps",
		"The caps of the source stream",
		gst.TypeCaps,
		glib.ParameterReadWrite,
	),
	glib.NewBoolParam(
		"has-handle",
		"has-handle",
		"check if a handle has been set",
		false,
		glib.ParameterReadable,
	),
}

// Here we declare a private struct to hold our internal state.
type state struct {
	writer io.WriteCloser
	closed atomic.Bool
}

// This is another private struct where we hold the parameter values set on our
// element.
type settings struct {
	caps *gst.Caps
}

type sinkWriter struct {
	self *base.GstBaseSink
	// The settings for the element
	settings *settings
	// The current state of the element
	state *state
}

func (*sinkWriter) New() glib.GoObjectSubclass {
	s := &sinkWriter{}
	return s
}

func (*sinkWriter) ClassInit(klass *glib.ObjectClass) {
	CAT.Log(gst.LevelDebug, "Initializing class")
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"Custom Writer Sink",
		"Sink",
		"Writes to a Go io.Writer",
		"Maxime SENARD <senard.maxime@gmail.com>",
	)

	CAT.Log(gst.LevelDebug, "Adding pad template")
	// Src pad template: ANY caps because we don't know what the reader contains
	class.AddPadTemplate(gst.NewPadTemplate(
		"sink",
		gst.PadDirectionSink,
		gst.PadPresenceAlways,
		gst.NewAnyCaps(),
	))

	class.InstallProperties(properties)
}

func (s *sinkWriter) Constructed(self *glib.Object) {
	s.self = base.ToGstBaseSink(self)

	s.self.Log(CAT, gst.LevelDebug, "Constructing")

	s.settings = &settings{
		caps: gst.NewAnyCaps(),
	}
	s.state = &state{
		writer: nil,
	}

	// s.self.SetSync(false)
}

func (s *sinkWriter) SetProperty(self *glib.Object, id uint, value *glib.Value) {
	param := properties[id]
	switch param.Name() {
	case "handle":
		gv, _ := value.GoValue()
		val, _ := gv.(uint64)
		h := cgo.Handle(uintptr(val))
		if h > 0 {
			obj := h.Value()
			if w, ok := obj.(io.WriteCloser); ok {
				s.state.writer = w
			}
		}
	case "caps":
		val, err := value.GoValue()
		if err != nil {
			s.self.Log(CAT, gst.LevelError, fmt.Sprintf("Error getting caps property value: %v", err))
			return
		}
		caps, ok := val.(*gst.Caps)
		if !ok {
			s.self.Log(CAT, gst.LevelError, "Invalid type for caps property")
			return
		}
		if caps == nil {
			s.self.Log(CAT, gst.LevelError, "Nil caps provided")
			return
		}
		s.settings.caps = caps.Copy()
		s.self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Element caps set to: %v", caps))
		if s.self != nil {
			s.self.GetStaticPad("sink").MarkReconfigure()
		}
	}
}

func (s *sinkWriter) GetProperty(self *glib.Object, id uint) *glib.Value {
	s.self.Log(CAT, gst.LevelInfo, "GetProperty called")
	param := properties[id]
	switch param.Name() {
	case "caps":
		if s.settings.caps != nil {
			v, _ := glib.GValue(s.settings.caps)
			return v
		}
		v, _ := glib.GValue(gst.NewAnyCaps())
		return v
	case "has-handle":
		hasHandle := s.state.writer != nil
		v, _ := glib.GValue(hasHandle)
		return v
	}
	return nil
}

func (s *sinkWriter) SetCaps(self *base.GstBaseSink, caps *gst.Caps) bool {
	s.self.Log(CAT, gst.LevelDebug, fmt.Sprintf("caps not set to: %s, keepping existing: %s", caps.String(), s.settings.caps.String()))
	return true
}

func (s *sinkWriter) GetCaps(self *base.GstBaseSink, filter *gst.Caps) *gst.Caps {
	if filter != nil && filter.Instance() != nil && !filter.IsEmpty() && !filter.IsAny() {
		s.self.Log(CAT, gst.LevelDebug, fmt.Sprintf("caps get filter: %s", filter.String()))
		if intersect := s.settings.caps.Intersect(filter); intersect != nil {
			return intersect
		}
	}
	s.self.Log(CAT, gst.LevelDebug, fmt.Sprintf("caps get: %s", s.settings.caps.String()))
	return s.settings.caps.Ref()
}

func (s *sinkWriter) Start(self *base.GstBaseSink) bool {
	s.self.Log(CAT, gst.LevelInfo, "started")
	return true
}

func (s *sinkWriter) closeWriter() bool {
	s.self.Log(CAT, gst.LevelInfo, "closeWriter")

	if s.state.closed.Swap(true) {
		s.self.Log(CAT, gst.LevelWarning, "io.Writer already closed")
		return true
	}

	if s.state.writer != nil {
		if err := s.state.writer.Close(); err != nil {
			s.self.Log(CAT, gst.LevelError, fmt.Sprintf("Error closing io.Writer: %v", err))
			s.self.ErrorMessage(gst.DomainResource, gst.ResourceErrorWrite, "Error closing io.Writer", err.Error())
			return false
		}
	}
	return true
}

func (s *sinkWriter) Stop(self *base.GstBaseSink) bool {
	s.self.Log(CAT, gst.LevelInfo, "stopped")

	if !s.closeWriter() {
		s.self.Log(CAT, gst.LevelError, "Error closing io.Writer")
		return false
	}

	return true
}

func (s *sinkWriter) Render(self *base.GstBaseSink, buffer *gst.Buffer) gst.FlowReturn {
	s.self.Log(CAT, gst.LevelTrace, fmt.Sprintf("Render called: buffer size=%d", buffer.GetSize()))
	if s.state.closed.Load() {
		s.self.Log(CAT, gst.LevelError, "io.Writer is already closed")
		s.self.ErrorMessage(gst.DomainResource, gst.ResourceErrorWrite, "io.Writer is already closed", "")
		return gst.FlowEOS
	}

	if s.state.writer == nil {
		s.self.Log(CAT, gst.LevelError, "io.Writer is not set")
		s.self.ErrorMessage(gst.DomainResource, gst.ResourceErrorSettings, "io.Writer is not set", "")
		return gst.FlowError
	}
	n, err := s.state.writer.Write(buffer.Bytes())
	if err != nil {
		if err == io.EOF {
			s.self.Log(CAT, gst.LevelInfo, "reached EOF on write")
			return gst.FlowEOS
		}
		s.self.Log(CAT, gst.LevelError, fmt.Sprintf("Error writing to io.Writer: %v", err))
		s.self.ErrorMessage(gst.DomainResource, gst.ResourceErrorWrite, "Error writing to io.Writer", err.Error())
		return gst.FlowError
	}
	if n < int(buffer.GetSize()) {
		s.self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Partial write to io.Writer: wrote %d of %d bytes", n, buffer.GetSize()))
	}

	s.self.Log(CAT, gst.LevelTrace, fmt.Sprintf("wrote %d bytes to io.Writer", n))
	return gst.FlowOK
}

func (s *sinkWriter) Unlock(self *base.GstBaseSink) bool {
	s.self.Log(CAT, gst.LevelInfo, "unlocked")

	if !s.closeWriter() {
		s.self.Log(CAT, gst.LevelError, "Error closing io.Writer")
		return false
	}
	return true
}

func (s *sinkWriter) UnlockStop(self *base.GstBaseSink) bool {
	s.self.Log(CAT, gst.LevelInfo, "unlock stopped")
	// Nothing to do here - we don't need to reset state since
	// once closed, the writer stays closed
	return true
}
