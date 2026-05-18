package vp9video

/*
#cgo pkg-config: gstreamer-1.0
#include <gst/gst.h>

extern void vp9_add_fix_pts_probe(GstElement *element);
*/
import "C"

import (
	"fmt"
	"unsafe"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
)

var CAT = gst.NewDebugCategory(
	"vp9-video",
	gst.DebugColorNone,
	"vp9-video Element",
)

var properties = []*glib.ParamSpec{
	glib.NewUintParam(
		"video-width",
		"Video Width",
		"Maximum width of the decoded video frames",
		1,
		8192,
		1280,
		glib.ParameterWritable|glib.ParameterConstructOnly,
	),
	glib.NewUintParam(
		"video-height",
		"Video Height",
		"Maximum height of the decoded video frames",
		1,
		8192,
		720,
		glib.ParameterWritable|glib.ParameterConstructOnly,
	),
}

type Vp9Video struct {
	videoWidth  uint
	videoHeight uint

	Vp9Depay   *gst.Element
	Vp9Parse   *gst.Element
	Vp9Dec     *gst.Element
	VideoScale *gst.Element
	VideoRate  *gst.Element
	Filter     *gst.Element
}

func (e *Vp9Video) New() glib.GoObjectSubclass {
	return &Vp9Video{}
}

func (e *Vp9Video) ClassInit(klass *glib.ObjectClass) {
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"VP9 to Video Decoder",
		"Video/Decoder",
		"Decodes VP9 RTP to raw video",
		"Roomkit <roomkit-visio@numerique.gouv.fr>",
	)

	class.AddPadTemplate(gst.NewPadTemplate(
		"sink",
		gst.PadDirectionSink,
		gst.PadPresenceAlways,
		gst.NewCapsFromString("application/x-rtp, media=(string)video, clock-rate=(int)90000, encoding-name=(string)VP9"),
	))

	class.AddPadTemplate(gst.NewPadTemplate(
		"src",
		gst.PadDirectionSource,
		gst.PadPresenceAlways,
		gst.NewCapsFromString("video/x-raw"),
	))

	class.InstallProperties(properties)
}

func (e *Vp9Video) InstanceInit(instance *glib.Object) {
	e.videoWidth = 1280
	e.videoHeight = 720
}

func (e *Vp9Video) Constructed(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	var err error

	e.Vp9Depay, err = gst.NewElementWithProperties("rtpvp9depay", map[string]interface{}{})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create rtpvp9depay element: %v", err))
		self.Error("Failed to create rtpvp9depay element", err)
		return
	}

	e.Vp9Parse, err = gst.NewElementWithProperties("vp9parse", map[string]interface{}{})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create vp9parse element: %v", err))
		self.Error("Failed to create vp9parse element", err)
		return
	}

	C.vp9_add_fix_pts_probe((*C.GstElement)(unsafe.Pointer(e.Vp9Parse.Instance())))

	e.Vp9Dec, err = gst.NewElementWithProperties("vp9dec", map[string]interface{}{})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create vp9dec element: %v", err))
		self.Error("Failed to create vp9dec element", err)
		return
	}

	e.VideoScale, err = gst.NewElementWithProperties("videoscale", map[string]interface{}{
		"add-borders": true,
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create videoscale element: %v", err))
		self.Error("Failed to create videoscale element", err)
		return
	}

	e.VideoRate, err = gst.NewElementWithProperties("videorate", map[string]interface{}{
		"drop-only": true,
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create videorate element: %v", err))
		self.Error("Failed to create videorate element", err)
		return
	}

	e.Filter, err = gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString(fmt.Sprintf("video/x-raw,width=[1,%d],height=[1,%d],pixel-aspect-ratio=1/1", e.videoWidth, e.videoHeight)),
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create capsfilter element: %v", err))
		self.Error("Failed to create capsfilter element", err)
		return
	}

	if err := self.AddMany(
		e.Vp9Depay,
		e.Vp9Parse,
		e.Vp9Dec,
		e.VideoScale,
		e.VideoRate,
		e.Filter,
	); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add elements to bin: %v", err))
		self.Error("Failed to add elements to bin", err)
		return
	}

	if err := gst.ElementLinkMany(
		e.Vp9Depay,
		e.Vp9Parse,
		e.Vp9Dec,
		e.VideoScale,
		e.VideoRate,
		e.Filter,
	); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link elements: %v", err))
		self.Error("Failed to link elements", err)
		return
	}

	elemClass := gst.ToElementClass(self.Class())

	ghostSink := gst.NewGhostPadFromTemplate("sink", e.Vp9Depay.GetStaticPad("sink"), elemClass.GetPadTemplate("sink"))
	self.AddPad(ghostSink.Pad)

	ghostSrc := gst.NewGhostPadFromTemplate("src", e.Filter.GetStaticPad("src"), elemClass.GetPadTemplate("src"))
	self.AddPad(ghostSrc.Pad)
}

func (e *Vp9Video) SetProperty(instance *glib.Object, id uint, value *glib.Value) {
	self := gst.ToGstBin(instance)
	param := properties[id]
	switch param.Name() {
	case "video-width":
		gv, err := value.GoValue()
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error getting video-width property value: %v", err))
			return
		}
		val, ok := gv.(uint)
		if !ok {
			self.Log(CAT, gst.LevelError, "Invalid type for video-width property")
			return
		}
		if val > 0xFFFF {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Invalid value for video-width property: %d", val))
			return
		}
		e.videoWidth = val
	case "video-height":
		gv, err := value.GoValue()
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error getting video-height property value: %v", err))
			return
		}
		val, ok := gv.(uint)
		if !ok {
			self.Log(CAT, gst.LevelError, "Invalid type for video-height property")
			return
		}
		if val > 0xFFFF {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Invalid value for video-height property: %d", val))
			return
		}
		e.videoHeight = val
	}
}

func (e *Vp9Video) Finalize(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	self.Log(CAT, gst.LevelDebug, "Finalizing Vp9Video element")

	e.Vp9Depay = nil
	e.Vp9Parse = nil
	e.Vp9Dec = nil
	e.VideoScale = nil
	e.VideoRate = nil
	e.Filter = nil
}
