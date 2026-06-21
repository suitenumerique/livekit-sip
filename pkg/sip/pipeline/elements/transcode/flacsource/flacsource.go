package flacsource

import (
	"fmt"
	"weak"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"golang.org/x/sys/unix"
)

var CAT = gst.NewDebugCategory(
	"flacsource",
	gst.DebugColorNone,
	"flacsource Element",
)

var properties = []*glib.ParamSpec{
	glib.NewIntParam(
		"fd",
		"FD",
		"File descriptor to read the FLAC bytes from. The bin closes this fd in Finalize.",
		-1,
		0x7FFFFFFF,
		-1,
		glib.ParameterReadable|glib.ParameterWritable|glib.ParameterConstructOnly,
	),
}

type FlacSource struct {
	fd int

	FdSrc         *gst.Element
	Queue         *gst.Element
	FlacParse     *gst.Element
	FlacDec       *gst.Element
	AudioConvert  *gst.Element
	AudioResample *gst.Element
	AudioRate     *gst.Element
	ClockSync     *gst.Element
}

func (e *FlacSource) New() glib.GoObjectSubclass {
	return &FlacSource{fd: -1}
}

func (e *FlacSource) ClassInit(klass *glib.ObjectClass) {
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"WAV Source",
		"Audio/Source",
		"Reads WAV bytes from a file descriptor and produces raw audio",
		"Roomkit <roomkit-visio@numerique.gouv.fr>",
	)

	class.AddPadTemplate(gst.NewPadTemplate(
		"src",
		gst.PadDirectionSource,
		gst.PadPresenceAlways,
		gst.NewCapsFromString("audio/x-raw"),
	))

	class.InstallProperties(properties)
}

func (e *FlacSource) InstanceInit(instance *glib.Object) {
	e.fd = -1
}

func (e *FlacSource) Constructed(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	var err error

	if e.fd < 0 {
		self.Log(CAT, gst.LevelError, "Invalid fd property: must be set to a non-negative integer")
		self.Error("Invalid fd property: must be set to a non-negative integer", nil)
		return
	}

	e.FdSrc, err = gst.NewElementWithProperties("fdsrc", map[string]interface{}{
		"fd": e.fd,
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create fdsrc element\nerr=%v", err))
		self.Error("Failed to create fdsrc element", err)
		return
	}

	e.Queue, err = gst.NewElementWithProperties("queue", map[string]interface{}{})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create queue element\nerr=%v", err))
		self.Error("Failed to create queue element", err)
		return
	}

	e.FlacParse, err = gst.NewElement("flacparse")
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create flacparse element\nerr=%v", err))
		self.Error("Failed to create flacparse element", err)
		return
	}

	e.FlacDec, err = gst.NewElement("flacdec")
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create flacdec element\nerr=%v", err))
		self.Error("Failed to create flacdec element", err)
		return
	}

	e.AudioConvert, err = gst.NewElement("audioconvert")
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create audioconvert element\nerr=%v", err))
		self.Error("Failed to create audioconvert element", err)
		return
	}

	e.AudioResample, err = gst.NewElement("audioresample")
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create audioresample element\nerr=%v", err))
		self.Error("Failed to create audioresample element", err)
		return
	}

	e.AudioRate, err = gst.NewElement("audiorate")
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create audiorate element\nerr=%v", err))
		self.Error("Failed to create audiorate element", err)
		return
	}

	e.ClockSync, err = gst.NewElement("clocksync")
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create clocksync element\nerr=%v", err))
		self.Error("Failed to create clocksync element", err)
		return
	}

	eweak := weak.Make(e)
	wself := glib.WeakRefInit(self)
	e.ClockSync.GetStaticPad("src").AddProbe(gst.PadProbeTypeBuffer, func(pad *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		e := eweak.Value()
		self := gst.ToGstBin(wself.Get())
		if e == nil || self == nil || self.Instance() == nil {
			return gst.PadProbeRemove
		}
		runningTime := self.GetCurrentRunningTime()
		pad.SetOffset(runningTime.Nanoseconds())
		return gst.PadProbeRemove
	})

	if err := self.AddMany(
		e.FdSrc,
		e.Queue,
		e.FlacParse,
		e.FlacDec,
		e.AudioConvert,
		e.AudioResample,
		e.AudioRate,
		e.ClockSync,
	); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add elements to bin\nerr=%v", err))
		self.Error("Failed to add elements to bin", err)
		return
	}

	if err := gst.ElementLinkMany(
		e.FdSrc,
		e.Queue,
		e.FlacParse,
		e.FlacDec,
		e.AudioConvert,
		e.AudioResample,
		e.AudioRate,
		e.ClockSync,
	); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link elements\nerr=%v", err))
		self.Error("Failed to link elements", err)
		return
	}

	elemClass := gst.ToElementClass(self.Class())
	ghostSrc := gst.NewGhostPadFromTemplate("src", e.ClockSync.GetStaticPad("src"), elemClass.GetPadTemplate("src"))
	self.AddPad(ghostSrc.Pad)
}

func (e *FlacSource) SetProperty(instance *glib.Object, id uint, value *glib.Value) {
	self := gst.ToGstBin(instance)
	param := properties[id]
	switch param.Name() {
	case "fd":
		gv, err := value.GoValue()
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error getting fd property value\nerr=%v", err))
			return
		}
		val, ok := gv.(int)
		if !ok {
			self.Log(CAT, gst.LevelError, "Invalid type for fd property")
			return
		}
		e.fd = val
	default:
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Unknown property\nproperty=%s", param.Name()))
	}
}

func (e *FlacSource) GetProperty(instance *glib.Object, id uint) *glib.Value {
	self := gst.ToGstBin(instance)
	param := properties[id]
	switch param.Name() {
	case "fd":
		value, err := glib.GValue(e.fd)
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error getting fd property value\nerr=%v", err))
			return nil
		}
		return value
	default:
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Unknown property\nproperty=%s", param.Name()))
		return nil
	}
}

func (e *FlacSource) Finalize(instance *glib.Object) {
	if e.fd >= 0 {
		unix.Close(e.fd)
		e.fd = -1
	}

	e.FdSrc = nil
	e.Queue = nil
	e.FlacParse = nil
	e.FlacDec = nil
	e.AudioConvert = nil
	e.AudioResample = nil
	e.AudioRate = nil
	e.ClockSync = nil
}
