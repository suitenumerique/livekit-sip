package iolivekit

import (
	"fmt"
	"sync"
	"weak"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"golang.org/x/sys/unix"
)

var CAT = gst.NewDebugCategory(
	"iolivekit",
	gst.DebugColorNone,
	"livekit SIP pipeline LiveKit IO element",
)

const AudioCaps = "audio/x-raw,format=S16LE,rate=16000,channels=1,layout=interleaved"

type IoManagerLivekit struct {
	inMu  sync.Mutex
	outMu sync.Mutex

	Compositor *gst.Element

	videoWidth     uint
	videoHeight    uint
	videoFramerate uint
	lang           string

	AudioIn  map[string]*AudioInTranscode
	AudioOut *AudioOutTranscode

	CameraIn  map[string]*CameraInTranscode
	CameraOut *CameraOutTranscode

	ScreenShareIn  map[string]*ScreenShareInTranscode
	ScreenShareOut *ScreenShareOutTranscode

	ScreenShareAudioIn map[string]*ScreenShareAudioInTranscode
}

type AudioInTranscode struct {
	gpad     *gst.GhostPad
	RtpAudio *gst.Element
	Filter   *gst.Element
	pad      *gst.Pad
}

type AudioOutTranscode struct {
	gpad     *gst.GhostPad
	Queue    *gst.Element
	AudioRtp *gst.Element
	pad      *gst.Pad
}

type CameraInTranscode struct {
	gpad     *gst.GhostPad
	RTPVideo *gst.Element
	Queue    *gst.Element
	pad      *gst.Pad
}

type CameraOutTranscode struct {
	gpad     *gst.GhostPad
	Queue    *gst.Element
	VideoRTP *gst.Element
	pad      *gst.Pad
}

type ScreenShareInTranscode struct {
	gpad     *gst.GhostPad
	RTPVideo *gst.Element
	Queue    *gst.Element
	pad      *gst.Pad
}

type ScreenShareOutTranscode struct {
	gpad     *gst.GhostPad
	Queue    *gst.Element
	VideoRTP *gst.Element
	pad      *gst.Pad
}

type ScreenShareAudioInTranscode struct {
	gpad     *gst.GhostPad
	RtpAudio *gst.Element
	Filter   *gst.Element
	pad      *gst.Pad
}

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
	glib.NewStringParam(
		"lang",
		"Language",
		"Language code for localized overlay text (e.g. en, fr)",
		nil,
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
}

func (e *IoManagerLivekit) New() glib.GoObjectSubclass {
	return &IoManagerLivekit{}
}

func (e *IoManagerLivekit) ClassInit(klass *glib.ObjectClass) {
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"io_manager_livekit",
		"Audio/Video/Converter",
		"Manages the input and output of the LiveKit pipeline",
		"Roomkit <roomkit-visio@numerique.gouv.fr>",
	)

	gst.SignalNew(
		class.Type(),
		"active-speakers-changed",
		gst.SignalRunLast,
		glib.TYPE_NONE,
		gst.TypeStructure, // TrackSourceInfo
	)

	gst.SignalNew(
		class.Type(),
		"participant-join",
		gst.SignalRunLast,
		glib.TYPE_NONE,
		gst.TypeStructure, // livekittracks.ParticipantInfo
	)

	gst.SignalNew(
		class.Type(),
		"participant-left",
		gst.SignalRunLast,
		glib.TYPE_NONE,
		gst.TypeStructure, // livekittracks.ParticipantInfo
	)

	gst.SignalNew(
		class.Type(),
		"has-screenshare",
		gst.SignalRunLast,
		glib.TYPE_NONE,
		glib.TYPE_BOOLEAN,
	)

	gst.SignalNew(
		class.Type(),
		"play-audio-fd",
		gst.SignalRunLast|gst.SignalAction,
		glib.TYPE_BOOLEAN,
		glib.TYPE_INT,
	)

	class.AddPadTemplate(gst.NewPadTemplate(
		"recv_rtp_sink_%u_%u_%u",
		gst.PadDirectionSink,
		gst.PadPresenceRequest,
		gst.NewCapsFromString("application/x-rtp"),
	))

	class.AddPadTemplate(gst.NewPadTemplate(
		"send_rtp_src_%u",
		gst.PadDirectionSource,
		gst.PadPresenceSometimes,
		gst.NewCapsFromString("application/x-rtp"),
	))

	class.InstallProperties(properties)

}

func (e *IoManagerLivekit) InstanceInit(instance *glib.Object) {
	e.AudioIn = make(map[string]*AudioInTranscode)
	e.CameraIn = make(map[string]*CameraInTranscode)
	e.ScreenShareIn = make(map[string]*ScreenShareInTranscode)
	e.ScreenShareAudioIn = make(map[string]*ScreenShareAudioInTranscode)
	e.videoWidth = 1280
	e.videoHeight = 720
	e.videoFramerate = 24
	e.lang = "en"
}

func (e *IoManagerLivekit) Constructed(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	eweak := weak.Make(e)
	wself := glib.WeakRefInit(self)

	var err error
	e.Compositor, err = gst.NewElementWithProperties("livekit_compositor", map[string]interface{}{
		"video-width":  e.videoWidth,
		"video-height": e.videoHeight,
		"framerate":    e.videoFramerate,
		"lang":         e.lang,
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create livekit_compositor element\nerr=%v", err))
		self.Error("Failed to create livekit_compositor element", err)
		return
	}
	if _, err := e.Compositor.Connect("pad-added", func(instance *gst.Element, pad *gst.Pad) {
		e := eweak.Value()
		self := gst.ToGstBin(wself.Get())
		if e != nil && self != nil && self.Instance() != nil {
			e.compositorPadAdded(self, pad)
		}
	}); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to connect to pad-added signal of livekit_compositor\nerr=%v", err))
		self.Error("Failed to connect to pad-added signal of livekit_compositor", err)
		return
	}
	if _, err := e.Compositor.Connect("pad-removed", func(instance *gst.Element, pad *gst.Pad) {
		e := eweak.Value()
		self := gst.ToGstBin(wself.Get())
		if e != nil && self != nil && self.Instance() != nil {
			e.compositorPadRemoved(self, pad)
		}
	}); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to connect to pad-removed signal of livekit_compositor\nerr=%v", err))
		self.Error("Failed to connect to pad-removed signal of livekit_compositor", err)
		return
	}

	if err := self.AddMany(e.Compositor); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add livekit_compositor element to SIP IO element\nerr=%v", err))
		self.Error("Failed to add livekit_compositor element to SIP IO element", err)
		return
	}

	if _, err := self.Connect("active-speakers-changed", func(instance *gst.Element, structure *gst.Structure) {
		e := eweak.Value()
		if e != nil && e.Compositor != nil {
			if _, err := e.Compositor.Emit("active-speakers-changed", structure); err != nil {
				self := gst.ToGstBin(instance)
				self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to forward active-speakers-changed signal from SIP IO element to compositor\nerr=%v", err))
				self.Error("Failed to forward active-speakers-changed signal from SIP IO element to compositor", err)
			}
		}
	}); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to connect to active-speakers-changed signal\nerr=%v", err))
		self.Error("Failed to connect to active-speakers-changed signal", err)
		return
	}

	if _, err := self.Connect("participant-join", func(instance *gst.Element, structure *gst.Structure) {
		e := eweak.Value()
		if e != nil && e.Compositor != nil {
			if _, err := e.Compositor.Emit("participant-join", structure); err != nil {
				self := gst.ToGstBin(instance)
				self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to forward participant-join signal from SIP IO element to compositor\nerr=%v", err))
				self.Error("Failed to forward participant-join signal from SIP IO element to compositor", err)
			}
		}
	}); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to connect to participant-join signal\nerr=%v", err))
		self.Error("Failed to connect to participant-join signal", err)
		return
	}

	if _, err := self.Connect("participant-left", func(instance *gst.Element, structure *gst.Structure) {
		e := eweak.Value()
		if e != nil && e.Compositor != nil {
			if _, err := e.Compositor.Emit("participant-left", structure); err != nil {
				self := gst.ToGstBin(instance)
				self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to forward participant-left signal from SIP IO element to compositor\nerr=%v", err))
				self.Error("Failed to forward participant-left signal from SIP IO element to compositor", err)
			}
		}
	}); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to connect to participant-left signal\nerr=%v", err))
		self.Error("Failed to connect to participant-left signal", err)
		return
	}

	if _, err := self.Connect("play-audio-fd", func(instance *gst.Element, fd int) bool {
		e := eweak.Value()
		self := gst.ToGstBin(wself.Get())
		if e == nil || self == nil || self.Instance() == nil {
			unix.Close(fd)
			return false
		}
		return e.playAudioFd(self, fd)
	}); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to connect to play-audio-fd signal\nerr=%v", err))
		self.Error("Failed to connect to play-audio-fd signal", err)
		return
	}
}

func (e *IoManagerLivekit) Finalize(instance *gst.Element) {
	e.inMu.Lock()
	e.outMu.Lock()
	defer e.inMu.Unlock()
	defer e.outMu.Unlock()

	e.Compositor = nil
	e.AudioIn = nil
	e.AudioOut = nil
	e.CameraIn = nil
	e.CameraOut = nil
	e.ScreenShareIn = nil
	e.ScreenShareOut = nil
}

func (e *IoManagerLivekit) SetProperty(instance *glib.Object, id uint, value *glib.Value) {
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
		if err := e.Compositor.SetProperty("microphone", value); err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to set microphone property on compositor\nerr=%v", err))
		}
	case "camera":
		if err := e.Compositor.SetProperty("camera", value); err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to set camera property on compositor\nerr=%v", err))
		}
	case "screenshare":
		if err := e.Compositor.SetProperty("screenshare", value); err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to set screenshare property on compositor\nerr=%v", err))
		}
	default:
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Unknown property ID\nid=%d", id))
	}
}
