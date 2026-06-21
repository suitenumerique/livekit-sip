package iosip

import (
	"fmt"
	"sync"
	"weak"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
)

var CAT = gst.NewDebugCategory(
	"iosip",
	gst.DebugColorNone,
	"livekit SIP pipeline SIP IO element",
)

type IoManagerSip struct {
	inMu  sync.Mutex
	outMu sync.Mutex

	Compositor     *gst.Element
	videoWidth     uint
	videoHeight    uint
	videoFramerate uint

	AudioIn  map[string]*SipAudioInTranscode
	AudioOut *SipAudioOutTranscode

	DtmfIn map[string]*SipDtmfInTranscode

	CameraIn  map[string]*SipCameraInTranscode
	CameraOut *SipCameraOutTranscode

	ScreenshareIn  map[string]*SipScreenshareInTranscode
	ScreenshareOut *SipScreenshareOutTranscode
}

type SipAudioInTranscode struct {
	gpad       *gst.GhostPad
	RtpAudio   *gst.Element
	DtmfDetect *gst.Element
	pad        *gst.Pad
}

type SipAudioOutTranscode struct {
	gpad     *gst.GhostPad
	AudioRtp *gst.Element
	pad      *gst.Pad
}

type SipDtmfInTranscode struct {
	gpad         *gst.GhostPad
	RtpDtmfDepay *gst.Element
	FakeSink     *gst.Element
}

type SipCameraInTranscode struct {
	gpad     *gst.GhostPad
	RTPVideo *gst.Element
	Queue    *gst.Element
	pad      *gst.Pad
}

type SipCameraOutTranscode struct {
	gpad     *gst.GhostPad
	Queue    *gst.Element
	VideoRTP *gst.Element
	pad      *gst.Pad
}

type SipScreenshareInTranscode struct {
	gpad     *gst.GhostPad
	RTPVideo *gst.Element
	Queue    *gst.Element
	pad      *gst.Pad
}

type SipScreenshareOutTranscode struct {
	gpad     *gst.GhostPad
	Queue    *gst.Element
	VideoRTP *gst.Element
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
}

func (e *IoManagerSip) New() glib.GoObjectSubclass {
	return &IoManagerSip{}
}

func (e *IoManagerSip) ClassInit(klass *glib.ObjectClass) {
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"io_manager_sip",
		"Audio/Video/Converter",
		"Manages the input and output of the SIP pipeline",
		"Roomkit <roomkit-visio@numerique.gouv.fr>",
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

func (e *IoManagerSip) InstanceInit(instance *glib.Object) {
	e.AudioIn = make(map[string]*SipAudioInTranscode)
	e.DtmfIn = make(map[string]*SipDtmfInTranscode)
	e.CameraIn = make(map[string]*SipCameraInTranscode)
	e.ScreenshareIn = make(map[string]*SipScreenshareInTranscode)
	e.videoWidth = 1280
	e.videoHeight = 720
	e.videoFramerate = 24
}

func (e *IoManagerSip) Constructed(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	eweak := weak.Make(e)
	wself := glib.WeakRefInit(self)

	var err error
	e.Compositor, err = gst.NewElementWithProperties("sip_compositor", map[string]interface{}{
		"video-width":  e.videoWidth,
		"video-height": e.videoHeight,
		"framerate":    e.videoFramerate,
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create sip_compositor element\nerr=%v", err))
		self.Error("Failed to create sip_compositor element", err)
		return
	}
	if _, err := e.Compositor.Connect("pad-added", func(instance *gst.Element, pad *gst.Pad) {
		e := eweak.Value()
		self := gst.ToGstBin(wself.Get())
		if e != nil && self != nil && self.Instance() != nil {
			e.compositorPadAdded(self, pad)
		}
	}); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to connect to pad-added signal of sip_compositor\nerr=%v", err))
		self.Error("Failed to connect to pad-added signal of sip_compositor", err)
		return
	}
	if _, err := e.Compositor.Connect("pad-removed", func(instance *gst.Element, pad *gst.Pad) {
		e := eweak.Value()
		self := gst.ToGstBin(wself.Get())
		if e != nil && self != nil && self.Instance() != nil {
			e.compositorPadRemoved(self, pad)
		}
	}); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to connect to pad-removed signal of sip_compositor\nerr=%v", err))
		self.Error("Failed to connect to pad-removed signal of sip_compositor", err)
		return
	}

	if err := self.Add(e.Compositor); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add sip_compositor element to SIP IO element\nerr=%v", err))
		self.Error("Failed to add sip_compositor element to SIP IO element", err)
		return
	}
}

func (e *IoManagerSip) Finalize(instance *glib.Object) {
	e.inMu.Lock()
	defer e.inMu.Unlock()
	e.outMu.Lock()
	defer e.outMu.Unlock()

	e.Compositor = nil
	e.AudioIn = nil
	e.AudioOut = nil
	e.DtmfIn = nil
	e.CameraIn = nil
	e.CameraOut = nil
	e.ScreenshareIn = nil
	e.ScreenshareOut = nil
}

func (e *IoManagerSip) SetProperty(instance *glib.Object, id uint, value *glib.Value) {
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
	}
}
