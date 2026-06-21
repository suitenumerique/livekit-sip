package videovp9

import (
	"fmt"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
)

var CAT = gst.NewDebugCategory(
	"video-vp9",
	gst.DebugColorNone,
	"video-vp9 Element",
)

var properties = []*glib.ParamSpec{
	glib.NewUintParam(
		"video-width",
		"Video Width",
		"Maximum width of the encoded video frames",
		1,
		8192,
		1280,
		glib.ParameterWritable|glib.ParameterConstructOnly,
	),
	glib.NewUintParam(
		"video-height",
		"Video Height",
		"Maximum height of the encoded video frames",
		1,
		8192,
		720,
		glib.ParameterWritable|glib.ParameterConstructOnly,
	),
}

type VideoVp9 struct {
	videoWidth  uint
	videoHeight uint

	VideoConvert *gst.Element
	VideoScale   *gst.Element
	Filter       *gst.Element
	Vp9Enc       *gst.Element
	Vp9Pay       *gst.Element
}

func (e *VideoVp9) New() glib.GoObjectSubclass {
	return &VideoVp9{}
}

func (e *VideoVp9) ClassInit(klass *glib.ObjectClass) {
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"Video to VP9 Encoder",
		"Video/Encoder",
		"Encodes raw video to VP9 RTP",
		"Roomkit <roomkit-visio@numerique.gouv.fr>",
	)

	class.AddPadTemplate(gst.NewPadTemplate(
		"sink",
		gst.PadDirectionSink,
		gst.PadPresenceAlways,
		gst.NewCapsFromString("video/x-raw"),
	))

	class.AddPadTemplate(gst.NewPadTemplate(
		"src",
		gst.PadDirectionSource,
		gst.PadPresenceAlways,
		gst.NewCapsFromString("application/x-rtp, media=(string)video, clock-rate=(int)90000, encoding-name=(string)VP9"),
	))

	class.InstallProperties(properties)
}

func (e *VideoVp9) InstanceInit(instance *glib.Object) {
	e.videoWidth = 1280
	e.videoHeight = 720
}

func (e *VideoVp9) Constructed(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	var err error

	e.VideoConvert, err = gst.NewElementWithProperties("videoconvert", map[string]interface{}{})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create videoconvert element\nerr=%v", err))
		self.Error("Failed to create videoconvert element", err)
		return
	}

	e.VideoScale, err = gst.NewElementWithProperties("videoscale", map[string]interface{}{
		"add-borders": true,
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create videoscale element\nerr=%v", err))
		self.Error("Failed to create videoscale element", err)
		return
	}

	e.Filter, err = gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString(fmt.Sprintf("video/x-raw,width=[1,%d],height=[1,%d],pixel-aspect-ratio=1/1", e.videoWidth, e.videoHeight)),
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create capsfilter element\nerr=%v", err))
		self.Error("Failed to create capsfilter element", err)
		return
	}

	e.Vp9Enc, err = gst.NewElementWithProperties("vp9enc", map[string]interface{}{
		// vp9enc realtime preset: deadline=1 + cpu-used=8 keeps the
		// encoder fast enough to avoid back-pressure; lag-in-frames=0
		// disables the 25-frame lookahead buffer that otherwise adds
		// ~1s of latency on the first frames of a stream.
		"deadline":      int(1),
		"cpu-used":      int(8),
		"lag-in-frames": int(0),
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create vp9enc element\nerr=%v", err))
		self.Error("Failed to create vp9enc element", err)
		return
	}

	e.Vp9Pay, err = gst.NewElementWithProperties("rtpvp9pay", map[string]interface{}{
		"mtu": int(1200),
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create rtpvp9pay element\nerr=%v", err))
		self.Error("Failed to create rtpvp9pay element", err)
		return
	}

	if err := self.AddMany(
		e.VideoConvert,
		e.VideoScale,
		e.Filter,
		e.Vp9Enc,
		e.Vp9Pay,
	); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add elements to bin\nerr=%v", err))
		self.Error("Failed to add elements to bin", err)
		return
	}

	if err := gst.ElementLinkMany(
		e.VideoConvert,
		e.VideoScale,
		e.Filter,
		e.Vp9Enc,
		e.Vp9Pay,
	); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link elements\nerr=%v", err))
		self.Error("Failed to link elements", err)
		return
	}

	elemClass := gst.ToElementClass(self.Class())

	ghostSink := gst.NewGhostPadFromTemplate("sink", e.VideoConvert.GetStaticPad("sink"), elemClass.GetPadTemplate("sink"))
	self.AddPad(ghostSink.Pad)

	ghostSrc := gst.NewGhostPadFromTemplate("src", e.Vp9Pay.GetStaticPad("src"), elemClass.GetPadTemplate("src"))
	self.AddPad(ghostSrc.Pad)
}

func (e *VideoVp9) SetProperty(instance *glib.Object, id uint, value *glib.Value) {
	self := gst.ToGstBin(instance)
	param := properties[id]
	switch param.Name() {
	case "video-width":
		gv, err := value.GoValue()
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error getting video-width property value\nerr=%v", err))
			return
		}
		val, ok := gv.(uint)
		if !ok {
			self.Log(CAT, gst.LevelError, "Invalid type for video-width property")
			return
		}
		if val > 0xFFFF {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Invalid value for video-width property\nvalue=%d", val))
			return
		}
		e.videoWidth = val
	case "video-height":
		gv, err := value.GoValue()
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error getting video-height property value\nerr=%v", err))
			return
		}
		val, ok := gv.(uint)
		if !ok {
			self.Log(CAT, gst.LevelError, "Invalid type for video-height property")
			return
		}
		if val > 0xFFFF {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Invalid value for video-height property\nvalue=%d", val))
			return
		}
		e.videoHeight = val
	}
}

func (e *VideoVp9) Finalize(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	self.Log(CAT, gst.LevelDebug, "Finalizing VideoVp9 element")

	e.VideoConvert = nil
	e.VideoScale = nil
	e.Filter = nil
	e.Vp9Enc = nil
	e.Vp9Pay = nil
}
