package videoh264

import (
	"fmt"
	"strconv"
	"sync"
	"time"
	"weak"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
)

var CAT = gst.NewDebugCategory(
	"video-h264",
	gst.DebugColorNone,
	"video-h264 Element",
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

type VideoH264 struct {
	videoWidth  uint
	videoHeight uint

	VideoConvert   *gst.Element
	VideoScale     *gst.Element
	ScaleFilter    *gst.Element
	X264Enc        *gst.Element
	H264RtpPayBin  *gst.Element
	RtpCodecFilter *gst.Element

	bitrateMu         sync.Mutex
	maxBitrate        uint
	curBitrate        uint
	lastBitrateAdjust time.Time
}

func (e *VideoH264) New() glib.GoObjectSubclass {
	return &VideoH264{}
}

func (e *VideoH264) ClassInit(klass *glib.ObjectClass) {
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"Video to H264 Encoder",
		"Video/Encoder",
		"Encodes raw video to H264 RTP",
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
		gst.NewCapsFromString("application/x-rtp, media=(string)video, encoding-name=(string)H264"),
	))

	class.InstallProperties(properties)
}

func (e *VideoH264) InstanceInit(instance *glib.Object) {
	e.videoWidth = 1280
	e.videoHeight = 720
}

func (e *VideoH264) Constructed(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	var err error

	wself := glib.WeakRefInit(self)
	eweak := weak.Make(e)

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

	// No pixel-aspect-ratio constraint: with PAR=1/1 + a range on both
	// dimensions, videoscale ends up picking odd widths (e.g. 853 for a
	// 1280x720 source targeting [1,854]x[1,480]) and x264enc refuses to
	// initialize on odd widths. Without the PAR constraint videoscale
	// fills the range exactly and picks even dimensions.
	e.ScaleFilter, err = gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString(fmt.Sprintf("video/x-raw,width=[1,%d],height=[1,%d]", e.videoWidth, e.videoHeight)),
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create scale capsfilter\nerr=%v", err))
		self.Error("Failed to create scale capsfilter", err)
		return
	}

	defaultBitrate := uint(2048)
	if e.videoHeight*e.videoWidth >= 1920*1080 {
		defaultBitrate = 8192
	} else if e.videoHeight*e.videoWidth >= 1280*720 {
		defaultBitrate = 4096
	}

	e.X264Enc, err = gst.NewElementWithProperties("x264enc", map[string]interface{}{
		"speed-preset":     int(1),  // ultrafast
		"tune":             uint(4), // zerolatency
		"key-int-max":      uint(200),
		"bframes":          uint(0),
		"vbv-buf-capacity": uint(2000),
		"bitrate":          uint(defaultBitrate),
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create x264enc element\nerr=%v", err))
		self.Error("Failed to create x264enc element", err)
		return
	}

	e.H264RtpPayBin, err = gst.NewElementWithProperties("h264rtppaybin", map[string]interface{}{})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create h264rtppaybin element\nerr=%v", err))
		self.Error("Failed to create h264rtppaybin element", err)
		return
	}
	if _, err := e.H264RtpPayBin.Connect("max-resolution", func(_ *gst.Element, w, h int) {
		self := gst.ToGstBin(wself.Get())
		if self == nil {
			return
		}
		e := eweak.Value()
		if e == nil {
			return
		}
		w = max(1, min(w, int(e.videoWidth)))
		h = max(1, min(h, int(e.videoHeight)))
		if err := e.ScaleFilter.SetProperty("caps", gst.NewCapsFromString(fmt.Sprintf("video/x-raw,width=[1,%d],height=[1,%d]", w, h))); err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to set scale filter caps\nerr=%v", err))
			self.Error("Failed to set scale filter caps", err)
		}
	}); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to connect max-resolution signal\nerr=%v", err))
		self.Error("Failed to connect max-resolution signal", err)
	}

	e.RtpCodecFilter, err = gst.NewElementWithProperties("rtpcapscodecfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString("application/x-rtp, media=(string)video, encoding-name=(string)H264"),
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create RTP codec filter element\nerr=%v", err))
		self.Error("Failed to create RTP codec filter element", err)
		return
	}
	if _, err := e.RtpCodecFilter.GetStaticPad("sink").Connect("notify::caps", func(pad *gst.Pad, _ *glib.ParamSpec) {
		self := gst.ToGstBin(wself.Get())
		if self == nil {
			return
		}
		e := eweak.Value()
		if e == nil {
			return
		}
		caps := pad.CurrentCaps()
		if caps == nil || caps.IsEmpty() {
			return
		}
		s := caps.GetStructureAt(0)
		bitrate := 0
		if v, err := s.GetString("max-br"); err == nil {
			if n, convErr := strconv.Atoi(v); convErr == nil && n > 0 {
				bitrate = n
			}
		}
		if v, err := s.GetString("max-bandwidth"); err == nil {
			if n, convErr := strconv.Atoi(v); convErr == nil && n > 0 && (bitrate == 0 || n < bitrate) {
				bitrate = n
			}
		}
		if bitrate > 0 {
			e.bitrateMu.Lock()
			e.maxBitrate = uint(bitrate)
			e.curBitrate = uint(bitrate)
			e.bitrateMu.Unlock()
			if err := e.X264Enc.SetProperty("bitrate", uint(bitrate)); err != nil {
				self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to set x264enc bitrate\nerr=%v", err))
				self.Error("Failed to set x264enc bitrate", err)
			} else {
				self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Updated x264enc bitrate\nbitrate=%d", bitrate))
			}
		}
	}); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to connect notify::caps signal\nerr=%v", err))
		self.Error("Failed to connect notify::caps signal", err)
	}

	if err := self.AddMany(
		e.VideoConvert,
		e.VideoScale,
		e.ScaleFilter,
		e.X264Enc,
		e.H264RtpPayBin,
		e.RtpCodecFilter,
	); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add elements to bin\nerr=%v", err))
		self.Error("Failed to add elements to bin", err)
		return
	}

	if err := gst.ElementLinkMany(
		e.VideoConvert,
		e.VideoScale,
		e.ScaleFilter,
		e.X264Enc,
		e.H264RtpPayBin,
		e.RtpCodecFilter,
	); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link elements\nerr=%v", err))
		self.Error("Failed to link elements", err)
		return
	}

	elemClass := gst.ToElementClass(self.Class())

	ghostSink := gst.NewGhostPadFromTemplate("sink", e.VideoConvert.GetStaticPad("sink"), elemClass.GetPadTemplate("sink"))
	self.AddPad(ghostSink.Pad)

	ghostSrc := gst.NewGhostPadFromTemplate("src", e.RtpCodecFilter.GetStaticPad("src"), elemClass.GetPadTemplate("src"))
	self.AddPad(ghostSrc.Pad)

	ghostSrc.Pad.AddProbe(gst.PadProbeTypeEventUpstream, func(_ *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		ev := info.GetEvent()
		if ev == nil || !ev.HasName("vopenia-link-feedback") {
			return gst.PadProbeOK
		}
		self := gst.ToGstBin(wself.Get())
		e := eweak.Value()
		if self == nil || e == nil {
			return gst.PadProbeOK
		}
		e.onLinkFeedback(self, ev.GetStructure())
		return gst.PadProbeOK
	})
}

func (e *VideoH264) SetProperty(instance *glib.Object, id uint, value *glib.Value) {
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

func (e *VideoH264) Finalize(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	self.Log(CAT, gst.LevelDebug, "Finalizing VideoH264 element")

	e.VideoConvert = nil
	e.VideoScale = nil
	e.ScaleFilter = nil
	e.X264Enc = nil
	e.H264RtpPayBin = nil
	e.RtpCodecFilter = nil
}

func (e *VideoH264) onLinkFeedback(self *gst.Bin, st *gst.Structure) {
	if st == nil {
		return
	}
	tmmbr := structIntField(st, "tmmbr-kbps")
	loss := structIntField(st, "fraction-lost")

	e.bitrateMu.Lock()
	defer e.bitrateMu.Unlock()

	if e.maxBitrate == 0 {
		return
	}
	if e.curBitrate == 0 {
		e.curBitrate = e.maxBitrate
	}

	now := time.Now()
	if !e.lastBitrateAdjust.IsZero() && now.Sub(e.lastBitrateAdjust) < time.Second {
		return
	}
	e.lastBitrateAdjust = now

	const floor = uint(300)
	ceiling := e.maxBitrate
	if tmmbr > 0 && uint(tmmbr) < ceiling {
		ceiling = uint(tmmbr)
	}

	target := e.curBitrate
	if loss > 5 {
		target = target * 85 / 100
	} else {
		target += target * 5 / 100
	}
	if target > ceiling {
		target = ceiling
	}
	if target < floor {
		target = floor
	}
	if target == e.curBitrate {
		return
	}

	e.curBitrate = target
	if err := e.X264Enc.SetProperty("bitrate", target); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to set adaptive x264enc bitrate\nerr=%v", err))
		return
	}
	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Updated x264enc bitrate (adaptive)\nbitrate=%d\nceiling=%d\ntmmbr_kbps=%d\nfraction_lost=%d", target, ceiling, tmmbr, loss))
}

func structIntField(st *gst.Structure, key string) int {
	v, err := st.GetValue(key)
	if err != nil {
		return 0
	}
	switch n := v.(type) {
	case int:
		return n
	case int32:
		return int(n)
	case int64:
		return int(n)
	case uint:
		return int(n)
	case uint32:
		return int(n)
	default:
		return 0
	}
}
