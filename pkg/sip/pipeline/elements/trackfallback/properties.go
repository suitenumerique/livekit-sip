package trackfallback

import (
	"fmt"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/livekit"
)

var properties = []*glib.ParamSpec{
	glib.NewUintParam(
		"video-width",
		"Video Width",
		"The width of the video frames",
		1,
		8192,
		1280,
		glib.ParameterWritable|glib.ParameterConstructOnly,
	),
	glib.NewUintParam(
		"video-height",
		"Video Height",
		"The height of the video frames",
		1,
		8192,
		720,
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
	glib.NewBoolParam(
		"microphone",
		"Microphone",
		"Whether to subscribe to microphone tracks",
		false,
		glib.ParameterWritable,
	),
	glib.NewBoolParam(
		"camera",
		"Camera",
		"Whether to subscribe to camera tracks",
		false,
		glib.ParameterWritable,
	),
	glib.NewBoolParam(
		"screenshare",
		"Screen Share",
		"Whether to subscribe to screenshare tracks",
		false,
		glib.ParameterWritable,
	),
	glib.NewBoolParam(
		"screenshare-audio",
		"Screen Share Audio",
		"Whether to subscribe to screenshare audio tracks",
		false,
		glib.ParameterWritable,
	),
}

func (e *TrackFallback) setTrackProperty(self *gst.Bin, trackSource livekit.TrackSource, enabled bool) {
	e.Tracks[trackSource].enabled = enabled
	if enabled {
		e.startTrackFallback(self, trackSource)
	} else {
		e.stopTrackFallback(self, trackSource)
	}
}

func (e *TrackFallback) SetProperty(instance *glib.Object, id uint, value *glib.Value) {
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
	case "microphone":
		gv, err := value.GoValue()
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error getting microphone property value\nerr=%v", err))
			return
		}
		val, ok := gv.(bool)
		if !ok {
			self.Log(CAT, gst.LevelError, "Invalid type for microphone property")
			return
		}
		e.setTrackProperty(self, livekit.TrackSource_MICROPHONE, val)
	case "camera":
		gv, err := value.GoValue()
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error getting camera property value\nerr=%v", err))
			return
		}
		val, ok := gv.(bool)
		if !ok {
			self.Log(CAT, gst.LevelError, "Invalid type for camera property")
			return
		}
		e.setTrackProperty(self, livekit.TrackSource_CAMERA, val)
	case "screenshare":
		gv, err := value.GoValue()
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error getting screenshare property value\nerr=%v", err))
			return
		}
		val, ok := gv.(bool)
		if !ok {
			self.Log(CAT, gst.LevelError, "Invalid type for screenshare property")
			return
		}
		e.setTrackProperty(self, livekit.TrackSource_SCREEN_SHARE, val)
	case "screenshare-audio":
		gv, err := value.GoValue()
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error getting screenshare-audio property value\nerr=%v", err))
			return
		}
		val, ok := gv.(bool)
		if !ok {
			self.Log(CAT, gst.LevelError, "Invalid type for screenshare-audio property")
			return
		}
		e.setTrackProperty(self, livekit.TrackSource_SCREEN_SHARE_AUDIO, val)
	default:
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Unknown property ID\nid=%d", id))
	}
}
