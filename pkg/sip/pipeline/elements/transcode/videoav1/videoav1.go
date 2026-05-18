package videoav1

import (
	"fmt"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
)

var CAT = gst.NewDebugCategory(
	"video-av1",
	gst.DebugColorNone,
	"video-av1 Element",
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

type VideoAv1 struct {
	videoWidth  uint
	videoHeight uint

	VideoConvert *gst.Element
	VideoScale   *gst.Element
	Filter       *gst.Element
	AV1Enc       *gst.Element
	AV1Parse     *gst.Element
	AV1Pay       *gst.Element
}

func (e *VideoAv1) New() glib.GoObjectSubclass {
	return &VideoAv1{}
}

func (e *VideoAv1) ClassInit(klass *glib.ObjectClass) {
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"Video to AV1 Encoder",
		"Video/Encoder",
		"Encodes raw video to AV1 RTP",
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
		gst.NewCapsFromString("application/x-rtp, media=(string)video, clock-rate=(int)90000, encoding-name=(string)AV1"),
	))

	class.InstallProperties(properties)
}

func (e *VideoAv1) InstanceInit(instance *glib.Object) {
	e.videoWidth = 1280
	e.videoHeight = 720
}

func (e *VideoAv1) Constructed(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	var err error

	e.VideoConvert, err = gst.NewElementWithProperties("videoconvert", map[string]interface{}{})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create videoconvert element: %v", err))
		self.Error("Failed to create videoconvert element", err)
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

	e.Filter, err = gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString(fmt.Sprintf("video/x-raw,width=[1,%d],height=[1,%d],pixel-aspect-ratio=1/1", e.videoWidth, e.videoHeight)),
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create capsfilter element: %v", err))
		self.Error("Failed to create capsfilter element", err)
		return
	}

	e.AV1Enc, err = gst.NewElementWithProperties("av1enc", map[string]interface{}{
		"cpu-used":      int(10),
		"threads":       uint(10),
		"usage-profile": int(1), // realtime
		"row-mt":        true,
		"tile-columns":  uint(3),
		"tile-rows":     uint(2),
		"end-usage":     int(1), // cbr
		"target-bitrate": uint(2000),
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create av1enc element: %v", err))
		self.Error("Failed to create av1enc element", err)
		return
	}

	e.AV1Parse, err = gst.NewElementWithProperties("av1parse", map[string]interface{}{})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create av1parse element: %v", err))
		self.Error("Failed to create av1parse element", err)
		return
	}

	e.AV1Pay, err = gst.NewElementWithProperties("rtpav1pay", map[string]interface{}{
		"mtu": int(1200),
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create rtpav1pay element: %v", err))
		self.Error("Failed to create rtpav1pay element", err)
		return
	}

	if err := self.AddMany(
		e.VideoConvert,
		e.VideoScale,
		e.Filter,
		e.AV1Enc,
		e.AV1Parse,
		e.AV1Pay,
	); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add elements to bin: %v", err))
		self.Error("Failed to add elements to bin", err)
		return
	}

	if err := gst.ElementLinkMany(
		e.VideoConvert,
		e.VideoScale,
		e.Filter,
		e.AV1Enc,
		e.AV1Parse,
		e.AV1Pay,
	); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link elements: %v", err))
		self.Error("Failed to link elements", err)
		return
	}

	elemClass := gst.ToElementClass(self.Class())

	ghostSink := gst.NewGhostPadFromTemplate("sink", e.VideoConvert.GetStaticPad("sink"), elemClass.GetPadTemplate("sink"))
	self.AddPad(ghostSink.Pad)

	ghostSrc := gst.NewGhostPadFromTemplate("src", e.AV1Pay.GetStaticPad("src"), elemClass.GetPadTemplate("src"))
	self.AddPad(ghostSrc.Pad)
}

func (e *VideoAv1) SetProperty(instance *glib.Object, id uint, value *glib.Value) {
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

func (e *VideoAv1) Finalize(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	self.Log(CAT, gst.LevelDebug, "Finalizing VideoAv1 element")

	e.VideoConvert = nil
	e.VideoScale = nil
	e.Filter = nil
	e.AV1Enc = nil
	e.AV1Parse = nil
	e.AV1Pay = nil
}
