package livekitcompositor

import (
	"fmt"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
)

var properties = []*glib.ParamSpec{
	glib.NewBoxedParam(
		"current-layout",
		"Current Layout",
		"The currently active layout",
		glib.TYPE_STRV,
		glib.ParameterReadable,
	),
	glib.NewUintParam(
		"video-width",
		"Video Width",
		"The width of the video frames",
		1,
		8192,
		1280,
		glib.ParameterReadable|glib.ParameterWritable|glib.ParameterConstructOnly,
	),
	glib.NewUintParam(
		"video-height",
		"Video Height",
		"The height of the video frames",
		1,
		8192,
		720,
		glib.ParameterReadable|glib.ParameterWritable|glib.ParameterConstructOnly,
	),
	glib.NewUintParam(
		"framerate",
		"Video Framerate",
		"The framerate of the video frames",
		1,
		500,
		24,
		glib.ParameterReadable|glib.ParameterWritable|glib.ParameterConstructOnly,
	),
	glib.NewStringParam(
		"lang",
		"Language",
		"Language code for localized overlay text (e.g. en, fr)",
		nil,
		glib.ParameterReadable|glib.ParameterWritable|glib.ParameterConstructOnly,
	),
	glib.NewBoolParam(
		"microphone",
		"Microphone",
		"Whether to subscribe to microphone tracks",
		false,
		glib.ParameterReadable|glib.ParameterWritable,
	),
	glib.NewBoolParam(
		"camera",
		"Camera",
		"Whether to subscribe to camera tracks",
		false,
		glib.ParameterReadable|glib.ParameterWritable,
	),
	glib.NewBoolParam(
		"screenshare",
		"Screen Share",
		"Whether to subscribe to screenshare tracks",
		false,
		glib.ParameterReadable|glib.ParameterWritable,
	),
}

func (e *LivekitCompositor) SetProperty(instance *glib.Object, id uint, value *glib.Value) {
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
		if val > 0xFFFF {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Invalid value for framerate property\nvalue=%d", val))
			return
		}
		e.videoFramerate = val
	case "lang":
		gv, err := value.GoValue()
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error getting lang property value\nerr=%v", err))
			return
		}
		val, ok := gv.(string)
		if !ok {
			self.Log(CAT, gst.LevelError, "Invalid type for lang property")
			return
		}
		if val != "" {
			e.lang = val
		}
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
		if val {
			if err := e.initMicrophone(self); err != nil {
				self.Log(CAT, gst.LevelError, fmt.Sprintf("Error initializing microphone\nerr=%v", err))
				return
			}
		} else {
			e.cleanupMicrophone(self)
		}
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
		if val {
			if err := e.initCamera(self); err != nil {
				self.Log(CAT, gst.LevelError, fmt.Sprintf("Error initializing camera\nerr=%v", err))
				return
			}
		} else {
			e.cleanupCamera(self)
		}
	case "screenshare":
		// unlike microphone and camera, screenshare is initialized and cleaned up on demand when sink pads are requested, so we don't need to do anything here
	default:
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Unknown property\nproperty=%s", param.Name()))
	}
}

func (e *LivekitCompositor) GetProperty(instance *glib.Object, id uint) *glib.Value {
	self := gst.ToGstBin(instance)
	param := properties[id]
	switch param.Name() {
	case "current-layout":
		e.mu.Lock()
		defer e.mu.Unlock()
		value, err := glib.GValue(glib.NewStrv(e.currentLayout))
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error getting current-layout property value\nerr=%v", err))
			return nil
		}
		return value
	case "framerate":
		value, err := glib.GValue(e.videoFramerate)
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error getting framerate property value\nerr=%v", err))
			return nil
		}
		return value
	case "video-width":
		value, err := glib.GValue(e.videoWidth)
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error getting video-width property value\nerr=%v", err))
			return nil
		}
		return value
	case "video-height":
		value, err := glib.GValue(e.videoHeight)
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error getting video-height property value\nerr=%v", err))
			return nil
		}
		return value
	case "lang":
		value, err := glib.GValue(e.lang)
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error getting lang property value\nerr=%v", err))
			return nil
		}
		return value
	case "microphone":
		value, err := glib.GValue(e.LivekitCompositorMicrophone != nil)
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error getting microphone property value\nerr=%v", err))
			return nil
		}
		return value
	case "camera":
		value, err := glib.GValue(e.LivekitCompositorCamera != nil)
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error getting camera property value\nerr=%v", err))
			return nil
		}
		return value
	case "screenshare":
		value, err := glib.GValue(e.LivekitCompositorScreenshare != nil)
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error getting screenshare property value\nerr=%v", err))
			return nil
		}
		return value
	default:
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Unknown property\nproperty=%s", param.Name()))
		return nil
	}
}
