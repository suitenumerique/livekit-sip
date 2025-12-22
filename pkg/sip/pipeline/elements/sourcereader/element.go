package sourcereader

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
	"sourcereader",
	gst.DebugColorFgYellow,
	"sourcereader Element",
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
	reader io.ReadCloser
	closed atomic.Bool
}

// This is another private struct where we hold the parameter values set on our
// element.
type settings struct {
	caps *gst.Caps
}

type SourceReader struct {
	self *base.GstBaseSrc
	// The settings for the element
	settings *settings
	// The current state of the element
	state *state
}

func (*SourceReader) New() glib.GoObjectSubclass {
	CAT.Log(gst.LevelInfo, "Creating new sourcereader instance")
	sr := &SourceReader{}
	return sr
}

func (*SourceReader) ClassInit(klass *glib.ObjectClass) {
	CAT.Log(gst.LevelDebug, "Initializing class")
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"Custom Reader Source",
		"Source",
		"Reads from a Go io.Reader",
		"Maxime SENARD <senard.maxime@gmail.com>",
	)

	CAT.Log(gst.LevelDebug, "Adding pad template")
	// Src pad template: ANY caps because we don't know what the reader contains
	class.AddPadTemplate(gst.NewPadTemplate(
		"src",
		gst.PadDirectionSource,
		gst.PadPresenceAlways,
		gst.NewAnyCaps(),
	))

	class.InstallProperties(properties)
}

func (s *SourceReader) Constructed(self *glib.Object) {
	s.self = base.ToGstBaseSrc(self)

	s.self.Log(CAT, gst.LevelDebug, "Constructing")

	s.settings = &settings{
		caps: gst.NewAnyCaps(),
	}
	s.state = &state{
		reader: nil,
	}

	s.self.SetFormat(gst.FormatTime)
	s.self.SetLive(true)
}

func (s *SourceReader) SetProperty(self *glib.Object, id uint, value *glib.Value) {
	param := properties[id]
	switch param.Name() {
	case "handle":
		gv, _ := value.GoValue()
		val, _ := gv.(uint64)
		h := cgo.Handle(val)
		if h > 0 {
			obj := h.Value()
			if r, ok := obj.(io.ReadCloser); ok {
				s.state.reader = r
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
			s.self.GetStaticPad("src").MarkReconfigure()
		}
	}
}

func (s *SourceReader) GetProperty(self *glib.Object, id uint) *glib.Value {
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
		hasHandle := s.state.reader != nil
		v, _ := glib.GValue(hasHandle)
		return v
	}
	return nil
}

func (s *SourceReader) SetCaps(self *base.GstBaseSrc, caps *gst.Caps) bool {
	s.self.Log(CAT, gst.LevelDebug, fmt.Sprintf("caps not set to: %s, keepping existing: %s", caps.String(), s.settings.caps.String()))
	return true
}

func (s *SourceReader) GetCaps(self *base.GstBaseSrc, filter *gst.Caps) *gst.Caps {
	if filter != nil && filter.Instance() != nil && !filter.IsEmpty() && !filter.IsAny() {
		s.self.Log(CAT, gst.LevelDebug, fmt.Sprintf("caps get filter: %s", filter.String()))
		if intersect := s.settings.caps.Intersect(filter); intersect != nil {
			return intersect
		}
	}
	s.self.Log(CAT, gst.LevelDebug, fmt.Sprintf("caps get: %s", s.settings.caps.String()))
	return s.settings.caps.Ref()
}

func (s *SourceReader) Start(self *base.GstBaseSrc) bool {
	s.self.Log(CAT, gst.LevelInfo, "started")
	return true
}

func (s *SourceReader) closeReader() bool {
	s.self.Log(CAT, gst.LevelInfo, "closeReader")

	if s.state.closed.Swap(true) {
		s.self.Log(CAT, gst.LevelWarning, "io.Reader already closed")
		return true
	}

	if s.state.reader != nil {
		if err := s.state.reader.Close(); err != nil {
			s.self.Log(CAT, gst.LevelError, fmt.Sprintf("Error closing io.Reader: %v", err))
			s.self.ErrorMessage(gst.DomainResource, gst.ResourceErrorRead, "Error closing io.Reader", err.Error())
			return false
		}
	}
	return true
}

func (s *SourceReader) Stop(self *base.GstBaseSrc) bool {
	s.self.Log(CAT, gst.LevelInfo, "stopped")

	if !s.closeReader() {
		s.self.Log(CAT, gst.LevelError, "Error closing io.Reader")
		return false
	}

	return true
}

func (s *SourceReader) Fill(self *base.GstBaseSrc, offset uint64, length uint, buffer *gst.Buffer) gst.FlowReturn {
	s.self.Log(CAT, gst.LevelTrace, fmt.Sprintf("Fill called: offset=%d, length=%d", offset, length))

	if s.state.closed.Load() {
		s.self.Log(CAT, gst.LevelError, "io.Reader is already closed")
		s.self.ErrorMessage(gst.DomainResource, gst.ResourceErrorRead, "io.Reader is already closed", "")
		return gst.FlowEOS
	}

	if s.state.reader == nil {
		s.self.Log(CAT, gst.LevelError, "io.Reader is not set")
		s.self.ErrorMessage(gst.DomainResource, gst.ResourceErrorSettings, "io.Reader is not set", "")
		return gst.FlowError
	}

	data := make([]byte, length)
	n, err := s.state.reader.Read(data)
	if err != nil {
		if s.state.closed.Load() {
			s.self.Log(CAT, gst.LevelInfo, "io.Reader closed during read")
			return gst.FlowFlushing
		}
		if err == io.EOF {
			s.self.Log(CAT, gst.LevelInfo, "reached EOF")
			return gst.FlowEOS
		}
		s.self.Log(CAT, gst.LevelError, fmt.Sprintf("Error reading from io.Reader: %v", err))
		s.self.ErrorMessage(gst.DomainResource, gst.ResourceErrorRead, "Error reading from io.Reader", err.Error())
		return gst.FlowError
	}

	buffer.FillBytes(0, data[:n])
	if uint(n) < length {
		buffer.SetSize(int64(n))
	}
	s.self.Log(CAT, gst.LevelTrace, fmt.Sprintf("filled buffer with %d bytes", n))

	return gst.FlowOK
}

func (s *SourceReader) Unlock(self *base.GstBaseSrc) bool {
	s.self.Log(CAT, gst.LevelInfo, "unlocked")

	if !s.closeReader() {
		s.self.Log(CAT, gst.LevelError, "Error closing io.Reader")
		return false
	}
	return true
}
