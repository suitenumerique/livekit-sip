package videovp8

import (
	"fmt"
	"time"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
)

var CAT = gst.NewDebugCategory(
	"video-vp8",
	gst.DebugColorNone,
	"video-vp8 Element",
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
	glib.NewStringParam(
		"usage",
		"Usage",
		"Content type being encoded: camera or screenshare",
		nil,
		glib.ParameterWritable|glib.ParameterConstructOnly,
	),
	glib.NewUintParam(
		"framerate",
		"Video Framerate",
		"The framerate of the video frames",
		1,
		500,
		24,
		glib.ParameterWritable|glib.ParameterConstructOnly,
	),
}

const UsageScreenshare = "screenshare"

type VideoVp8 struct {
	videoWidth     uint
	videoHeight    uint
	usage          string
	videoFramerate uint

	VideoConvert *gst.Element
	VideoScale   *gst.Element
	Filter       *gst.Element
	Vp8Enc       *gst.Element
	Vp8Pay       *gst.Element
}

func (e *VideoVp8) New() glib.GoObjectSubclass {
	return &VideoVp8{}
}

func (e *VideoVp8) ClassInit(klass *glib.ObjectClass) {
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"Video to VP8 Encoder",
		"Video/Encoder",
		"Encodes raw video to VP8 RTP",
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
		gst.NewCapsFromString("application/x-rtp, media=(string)video, clock-rate=(int)90000, encoding-name=(string)VP8"),
	))

	class.InstallProperties(properties)
}

func (e *VideoVp8) InstanceInit(instance *glib.Object) {
	e.videoWidth = 1280
	e.videoHeight = 720
	e.usage = "camera"
	e.videoFramerate = 24
}

func (e *VideoVp8) Constructed(instance *glib.Object) {
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

	pixels := e.videoWidth * e.videoHeight
	// cpu-used < 0 pins the libvpx realtime speed to -cpu-used; cpu-used >= 0
	// enables vp8_auto_select_speed, which spends (16-cpu-used)/16 of every
	// frame period whatever the content.
	targetBitrate, maxQuantizer, cpuUsed := 1_000_000, 56, -8
	switch {
	case pixels >= 1920*1080:
		targetBitrate = 3_500_000
	case pixels >= 1280*720:
		targetBitrate = 2_000_000
	}
	if e.usage == UsageScreenshare {
		targetBitrate, maxQuantizer, cpuUsed = 1_500_000, 40, -6
		switch {
		case pixels >= 1920*1080:
			targetBitrate = 6_000_000
		case pixels >= 1280*720:
			targetBitrate = 3_000_000
		}
	}
	vp8Props := map[string]interface{}{
		"deadline":                    int(1), // realtime
		"cpu-used":                    cpuUsed,
		"target-bitrate":              targetBitrate,
		"keyframe-max-dist":           int(4 * e.videoFramerate),
		"lag-in-frames":               int(0),
		"threads":                     int(4),
		"token-partitions":            int(2),
		"buffer-initial-size":         int(200),
		"buffer-optimal-size":         int(300),
		"buffer-size":                 int(500),
		"min-quantizer":               int(4),
		"max-quantizer":               maxQuantizer,
		"error-resilient":             int(1),
		"end-usage":                   int(1), // CBR
		"min-force-key-unit-interval": uint64(time.Second),
	}
	if e.usage == UsageScreenshare {
		// static-threshold skips re-encoding unchanged blocks.
		vp8Props["static-threshold"] = int(100)
	}
	e.Vp8Enc, err = gst.NewElementWithProperties("vp8enc", vp8Props)
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create vp8enc element\nerr=%v", err))
		self.Error("Failed to create vp8enc element", err)
		return
	}

	e.Vp8Pay, err = gst.NewElementWithProperties("rtpvp8pay", map[string]interface{}{
		"mtu":             int(1200),
		"picture-id-mode": int(2),
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create rtpvp8pay element\nerr=%v", err))
		self.Error("Failed to create rtpvp8pay element", err)
		return
	}

	if err := self.AddMany(
		e.VideoConvert,
		e.VideoScale,
		e.Filter,
		e.Vp8Enc,
		e.Vp8Pay,
	); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add elements to bin\nerr=%v", err))
		self.Error("Failed to add elements to bin", err)
		return
	}

	if err := gst.ElementLinkMany(
		e.VideoConvert,
		e.VideoScale,
		e.Filter,
		e.Vp8Enc,
		e.Vp8Pay,
	); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link elements\nerr=%v", err))
		self.Error("Failed to link elements", err)
		return
	}

	elemClass := gst.ToElementClass(self.Class())

	ghostSink := gst.NewGhostPadFromTemplate("sink", e.VideoConvert.GetStaticPad("sink"), elemClass.GetPadTemplate("sink"))
	self.AddPad(ghostSink.Pad)

	ghostSrc := gst.NewGhostPadFromTemplate("src", e.Vp8Pay.GetStaticPad("src"), elemClass.GetPadTemplate("src"))
	self.AddPad(ghostSrc.Pad)
}

func (e *VideoVp8) SetProperty(instance *glib.Object, id uint, value *glib.Value) {
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
	case "usage":
		gv, err := value.GoValue()
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error getting usage property value\nerr=%v", err))
			return
		}
		val, ok := gv.(string)
		if !ok {
			self.Log(CAT, gst.LevelError, "Invalid type for usage property")
			return
		}
		if val == "" {
			return
		}
		if val != "camera" && val != UsageScreenshare {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Invalid value for usage property\nvalue=%s", val))
			return
		}
		e.usage = val
	case "framerate":
		gv, err := value.GoValue()
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error getting framerate property value\nerr=%v", err))
			return
		}
		val, ok := gv.(uint)
		if !ok {
			self.Log(CAT, gst.LevelError, "Invalid type for framerate property")
			return
		}
		e.videoFramerate = val
	}
}

func (e *VideoVp8) Finalize(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	self.Log(CAT, gst.LevelDebug, "Finalizing VideoVp8 element")

	e.VideoConvert = nil
	e.VideoScale = nil
	e.Filter = nil
	e.Vp8Enc = nil
	e.Vp8Pay = nil
}
