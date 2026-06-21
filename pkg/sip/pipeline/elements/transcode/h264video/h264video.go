package h264video

/*
#cgo pkg-config: gstreamer-1.0 gstreamer-video-1.0
#include <gst/gst.h>
#include <gst/video/video.h>

typedef struct {
    GstClockTime last_request;   // monotonic ns; GST_CLOCK_TIME_NONE = never
    GstClockTime last_pts;       // PTS of previous output buffer
} h264_video_probe_state;

static GstPadProbeReturn
h264_video_probe_bad_buffers(GstPad *pad, GstPadProbeInfo *info, gpointer user_data) {
    GstBuffer *buf = GST_PAD_PROBE_INFO_BUFFER(info);
    if (G_UNLIKELY(buf == NULL))
        return GST_PAD_PROBE_OK;

    h264_video_probe_state *st = (h264_video_probe_state *)user_data;

    guint flags = 0;
    if (GST_BUFFER_FLAG_IS_SET(buf, GST_BUFFER_FLAG_DISCONT))
        flags |= 1;
    if (GST_BUFFER_FLAG_IS_SET(buf, GST_BUFFER_FLAG_CORRUPTED))
        flags |= 2;

    GstClockTime pts = GST_BUFFER_PTS(buf);
    if (GST_CLOCK_TIME_IS_VALID(pts) &&
        GST_CLOCK_TIME_IS_VALID(st->last_pts) &&
        pts <= st->last_pts) {
        flags |= 4;
    }
    if (GST_CLOCK_TIME_IS_VALID(pts))
        st->last_pts = pts;

    if (G_LIKELY(flags == 0))
        return GST_PAD_PROBE_OK;

    GstClockTime now = gst_util_get_timestamp();
    if (st->last_request == GST_CLOCK_TIME_NONE ||
        now - st->last_request > 5 * GST_SECOND) {
        gst_pad_send_event(pad,
            gst_video_event_new_upstream_force_key_unit(
                GST_CLOCK_TIME_NONE, TRUE, 0));
        st->last_request = now;
    }
    return GST_PAD_PROBE_OK;
}

static void
h264_video_add_probe_bad_buffers(GstPad *pad) {
    h264_video_probe_state *st = g_new(h264_video_probe_state, 1);
    st->last_request = GST_CLOCK_TIME_NONE;
    st->last_pts     = GST_CLOCK_TIME_NONE;
    gst_pad_add_probe(pad, GST_PAD_PROBE_TYPE_BUFFER,
        h264_video_probe_bad_buffers, st, g_free);
}
*/
import "C"

import (
	"fmt"
	"unsafe"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
)

var CAT = gst.NewDebugCategory(
	"h264-video",
	gst.DebugColorNone,
	"h264-video Element",
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

type H264Video struct {
	videoWidth  uint
	videoHeight uint

	H264Depay    *gst.Element
	H264Parse    *gst.Element
	H264Dec      *gst.Element
	VideoConvert *gst.Element
	VideoScale   *gst.Element
	VideoRate    *gst.Element
	Filter       *gst.Element
}

func (e *H264Video) New() glib.GoObjectSubclass {
	return &H264Video{}
}

func (e *H264Video) ClassInit(klass *glib.ObjectClass) {
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"H264 to Video Decoder",
		"Video/Decoder",
		"Decodes H264 RTP to raw video",
		"Roomkit <roomkit-visio@numerique.gouv.fr>",
	)

	class.AddPadTemplate(gst.NewPadTemplate(
		"sink",
		gst.PadDirectionSink,
		gst.PadPresenceAlways,
		gst.NewCapsFromString("application/x-rtp, media=(string)video, clock-rate=(int)90000, encoding-name=(string)H264"),
	))

	class.AddPadTemplate(gst.NewPadTemplate(
		"src",
		gst.PadDirectionSource,
		gst.PadPresenceAlways,
		gst.NewCapsFromString("video/x-raw"),
	))

	class.InstallProperties(properties)
}

func (e *H264Video) InstanceInit(instance *glib.Object) {
	e.videoWidth = 1280
	e.videoHeight = 720
}

func (e *H264Video) Constructed(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	var err error

	e.H264Depay, err = gst.NewElementWithProperties("rtph264depay", map[string]interface{}{
		"request-keyframe":  true,
		"wait-for-keyframe": false,
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create rtph264depay element\nerr=%v", err))
		self.Error("Failed to create rtph264depay element", err)
		return
	}

	e.H264Parse, err = gst.NewElementWithProperties("h264parse", map[string]interface{}{
		"config-interval": int(1),
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create h264parse element\nerr=%v", err))
		self.Error("Failed to create h264parse element", err)
		return
	}

	e.H264Dec, err = gst.NewElementWithProperties("avdec_h264", map[string]interface{}{
		"max-threads":                   int(4),
		"automatic-request-sync-points": true,
		"min-force-key-unit-interval":   uint64(0),
		"discard-corrupted-frames":      false,
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create avdec_h264 element\nerr=%v", err))
		self.Error("Failed to create avdec_h264 element", err)
		return
	}
	C.h264_video_add_probe_bad_buffers((*C.GstPad)(unsafe.Pointer(e.H264Dec.GetStaticPad("src").Instance())))

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

	e.VideoRate, err = gst.NewElementWithProperties("videorate", map[string]interface{}{
		"drop-only":     false,
		"skip-to-first": true,
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create videorate element\nerr=%v", err))
		self.Error("Failed to create videorate element", err)
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

	if err := self.AddMany(
		e.H264Depay,
		e.H264Parse,
		e.H264Dec,
		e.VideoConvert,
		e.VideoScale,
		e.VideoRate,
		e.Filter,
	); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add elements to bin\nerr=%v", err))
		self.Error("Failed to add elements to bin", err)
		return
	}

	if err := gst.ElementLinkMany(
		e.H264Depay,
		e.H264Parse,
		e.H264Dec,
		e.VideoConvert,
		e.VideoScale,
		e.VideoRate,
		e.Filter,
	); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link elements\nerr=%v", err))
		self.Error("Failed to link elements", err)
		return
	}

	elemClass := gst.ToElementClass(self.Class())

	ghostSink := gst.NewGhostPadFromTemplate("sink", e.H264Depay.GetStaticPad("sink"), elemClass.GetPadTemplate("sink"))
	self.AddPad(ghostSink.Pad)

	ghostSrc := gst.NewGhostPadFromTemplate("src", e.Filter.GetStaticPad("src"), elemClass.GetPadTemplate("src"))
	self.AddPad(ghostSrc.Pad)
}

func (e *H264Video) SetProperty(instance *glib.Object, id uint, value *glib.Value) {
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

func (e *H264Video) Finalize(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	self.Log(CAT, gst.LevelDebug, "Finalizing H264Video element")

	e.H264Depay = nil
	e.H264Parse = nil
	e.H264Dec = nil
	e.VideoConvert = nil
	e.VideoScale = nil
	e.VideoRate = nil
	e.Filter = nil
}
