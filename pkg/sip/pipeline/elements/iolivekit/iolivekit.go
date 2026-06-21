package iolivekit

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/livekit"
	"golang.org/x/sys/unix"
)

func (e *IoManagerLivekit) RequestNewPad(instance *gst.Element, templ *gst.PadTemplate, name string, caps *gst.Caps) *gst.Pad {
	self := gst.ToGstBin(instance)

	if templ == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Pad template is nil for pad\npad=%s", name))
		return nil
	}

	switch templ.Name() {
	case "recv_rtp_sink_%u_%u_%u":
		var session, ssrc, pt int
		if _, err := fmt.Sscanf(name, "recv_rtp_sink_%d_%d_%d", &session, &ssrc, &pt); err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to parse pad name\npad=%s\nerr=%v", name, err))
			return nil
		}

		if pt < 0 || pt > 127 {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Invalid payload type in pad name\npad=%s\npt=%d", name, pt))
			return nil
		}

		switch livekit.TrackSource(session) {
		case livekit.TrackSource_MICROPHONE:
			return e.requestNewPadAudioIn(self, templ, name, session, ssrc, pt)
		case livekit.TrackSource_CAMERA:
			return e.requestNewPadCameraIn(self, templ, name, session, ssrc, pt)
		case livekit.TrackSource_SCREEN_SHARE:
			return e.requestNewPadScreenShareIn(self, templ, name, session, ssrc, pt)
		case livekit.TrackSource_SCREEN_SHARE_AUDIO:
			return e.requestNewPadScreenShareAudioIn(self, templ, name, session, ssrc, pt)
		default:
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Unsupported session kind in pad name\npad=%s\nsession=%d\nsource=%s", name, session, livekit.TrackSource(session).String()))
			return nil
		}
	default:
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Unknown pad template for pad\ntemplate=%s\npad=%s", templ.Name(), name))
		return nil
	}
}

func (e *IoManagerLivekit) requestNewPadAudioIn(self *gst.Bin, templ *gst.PadTemplate, name string, session int, ssrc int, pt int) *gst.Pad {
	e.inMu.Lock()
	defer e.inMu.Unlock()

	if _, exists := e.AudioIn[name]; exists {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Pad with name already exists\nname=%s", name))
		return nil
	}

	audioIn := &AudioInTranscode{}

	var err error
	audioIn.RtpAudio, err = gst.NewElementWithProperties("factorybin", map[string]interface{}{
		"factories": glib.NewStrv([]string{
			"g722-audio",
			"opus-audio",
			"pcmu-audio",
			"pcma-audio",
		}),
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create factorybin element for pad\npad=%s\nerr=%v", name, err))
		self.Error(fmt.Sprintf("Failed to create factorybin element for pad %s", name), err)
		return nil
	}
	audioIn.Filter, err = gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString(AudioCaps),
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create capsfilter element for pad\npad=%s\nerr=%v", name, err))
		self.Error(fmt.Sprintf("Failed to create capsfilter element for pad %s", name), err)
		return nil
	}

	if err := self.AddMany(audioIn.RtpAudio, audioIn.Filter); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add factorybin element to SIP IO element for pad\npad=%s\nerr=%v", name, err))
		self.Error(fmt.Sprintf("Failed to add factorybin element to SIP IO element for pad %s", name), err)
		return nil
	}

	if err := audioIn.RtpAudio.Link(audioIn.Filter); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link factorybin element to capsfilter for pad\npad=%s\nerr=%v", name, err))
		self.Error(fmt.Sprintf("Failed to link factorybin element to capsfilter for pad %s", name), err)
		return nil
	}

	audioIn.pad = e.Compositor.GetRequestPad(fmt.Sprintf("sink_%d_%d_%d", session, ssrc, pt))
	if audioIn.pad == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get request pad from compositor for pad\npad=%s", name))
		self.Error(fmt.Sprintf("Failed to get request pad from compositor for pad %s", name), fmt.Errorf("compositor returned nil pad"))
		return nil
	}

	if ret := audioIn.Filter.GetStaticPad("src").Link(audioIn.pad); ret != gst.PadLinkOK {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link capsfilter src pad to compositor pad for pad\npad=%s\nret=%v", name, ret))
		self.Error(fmt.Sprintf("Failed to link capsfilter src pad to compositor pad for pad %s", name), fmt.Errorf("failed to link pads"))
		return nil
	}

	audioIn.gpad = gst.NewGhostPadFromTemplate(name, audioIn.RtpAudio.GetStaticPad("sink"), templ)
	if audioIn.gpad == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create ghost pad for pad\npad=%s", name))
		self.Error(fmt.Sprintf("Failed to create ghost pad for pad %s", name), fmt.Errorf("gst.NewGhostPadFromTemplate returned nil"))
		return nil
	}
	if !audioIn.gpad.SetActive(true) {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to activate ghost pad for pad\npad=%s", name))
	}
	if !self.AddPad(audioIn.gpad.Pad) {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add ghost pad to SIP IO element for pad\npad=%s", name))
		self.Error(fmt.Sprintf("Failed to add ghost pad to SIP IO element for pad %s", name), fmt.Errorf("self.AddPad returned false"))
		return nil
	}

	if !audioIn.RtpAudio.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to sync state of factorybin element with parent for pad\npad=%s", name))
	}
	if !audioIn.Filter.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to sync state of capsfilter element with parent for pad\npad=%s", name))
	}

	e.AudioIn[name] = audioIn

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Successfully created new audio input pad\npad=%s\nsession=%d", name, session))
	return audioIn.gpad.Pad
}

func (e *IoManagerLivekit) requestNewPadCameraIn(self *gst.Bin, templ *gst.PadTemplate, name string, session int, ssrc int, pt int) *gst.Pad {
	e.inMu.Lock()
	defer e.inMu.Unlock()

	if _, exists := e.CameraIn[name]; exists {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Pad with name already exists\nname=%s", name))
		return nil
	}

	cameraIn := &CameraInTranscode{}

	var err error
	properties := gst.NewStructure("properties")
	if err := errors.Join(
		properties.SetUint("*.video-width", e.videoWidth),
		properties.SetUint("*.video-height", e.videoHeight),
	); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to set properties for factorybin element for camera input pad\nerr=%v", err))
		self.Error("Failed to set properties for factorybin element for camera input pad", err)
		return nil
	}
	cameraIn.RTPVideo, err = gst.NewElementWithProperties("factorybin", map[string]interface{}{
		"factories": glib.NewStrv([]string{
			"vp9-video",
			"vp8-video",
			"h264-video",
		}),
		"child-properties": properties,
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create factorybin element for pad\npad=%s\nerr=%v", name, err))
		self.Error(fmt.Sprintf("Failed to create factorybin element for pad %s", name), err)
		return nil
	}
	cameraIn.Queue, err = gst.NewElementWithProperties("queue", map[string]interface{}{
		"max-size-buffers": 3,
		"max-size-bytes":   0,
		"max-size-time":    0,
		"leaky":            2, // downstream
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create queue element for pad\npad=%s\nerr=%v", name, err))
		self.Error(fmt.Sprintf("Failed to create queue element for pad %s", name), err)
		return nil
	}

	if err := self.AddMany(cameraIn.RTPVideo, cameraIn.Queue); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add factorybin and queue elements to SIP IO element for pad\npad=%s\nerr=%v", name, err))
		self.Error(fmt.Sprintf("Failed to add factorybin and queue elements to SIP IO element for pad %s", name), err)
		return nil
	}

	if err := cameraIn.RTPVideo.Link(cameraIn.Queue); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link factorybin element to queue for pad\npad=%s\nerr=%v", name, err))
		self.Error(fmt.Sprintf("Failed to link factorybin element to queue for pad %s", name), err)
		return nil
	}

	cameraIn.pad = e.Compositor.GetRequestPad(fmt.Sprintf("sink_%d_%d_%d", session, ssrc, pt))
	if cameraIn.pad == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get request pad from compositor for pad\npad=%s", name))
		self.Error(fmt.Sprintf("Failed to get request pad from compositor for pad %s", name), fmt.Errorf("compositor returned nil pad"))
		return nil
	}

	if ret := cameraIn.Queue.GetStaticPad("src").Link(cameraIn.pad); ret != gst.PadLinkOK {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link queue src pad to compositor pad for pad\npad=%s\nret=%v", name, ret))
		self.Error(fmt.Sprintf("Failed to link queue src pad to compositor pad for pad %s", name), fmt.Errorf("failed to link pads"))
		return nil
	}

	cameraIn.gpad = gst.NewGhostPadFromTemplate(name, cameraIn.RTPVideo.GetStaticPad("sink"), templ)
	if cameraIn.gpad == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create ghost pad for pad\npad=%s", name))
		self.Error(fmt.Sprintf("Failed to create ghost pad for pad %s", name), fmt.Errorf("gst.NewGhostPadFromTemplate returned nil"))
		return nil
	}
	if !cameraIn.gpad.SetActive(true) {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to activate ghost pad for pad\npad=%s", name))
	}
	if !self.AddPad(cameraIn.gpad.Pad) {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add ghost pad to SIP IO element for pad\npad=%s", name))
		self.Error(fmt.Sprintf("Failed to add ghost pad to SIP IO element for pad %s", name), fmt.Errorf("self.AddPad returned false"))
		return nil
	}

	if !cameraIn.RTPVideo.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to sync state of factorybin element with parent for pad\npad=%s", name))
	}
	if !cameraIn.Queue.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to sync state of queue element with parent for pad\npad=%s", name))
	}

	e.CameraIn[name] = cameraIn

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Successfully created new camera input pad\npad=%s\nsession=%d", name, session))
	return cameraIn.gpad.Pad
}

func (e *IoManagerLivekit) requestNewPadScreenShareIn(self *gst.Bin, templ *gst.PadTemplate, name string, session int, ssrc int, pt int) *gst.Pad {
	e.inMu.Lock()
	defer e.inMu.Unlock()

	if _, exists := e.ScreenShareIn[name]; exists {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Pad with name already exists\nname=%s", name))
		return nil
	}

	screenShareIn := &ScreenShareInTranscode{}

	var err error
	properties := gst.NewStructure("properties")
	if err := errors.Join(
		properties.SetUint("*.video-width", e.videoWidth),
		properties.SetUint("*.video-height", e.videoHeight),
	); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to set properties for factorybin element for screen share input pad\nerr=%v", err))
		self.Error("Failed to set properties for factorybin element for screen share input pad", err)
		return nil
	}
	screenShareIn.RTPVideo, err = gst.NewElementWithProperties("factorybin", map[string]interface{}{
		"factories": glib.NewStrv([]string{
			"vp9-video",
			"vp8-video",
			"h264-video",
		}),
		"child-properties": properties,
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create factorybin element for pad\npad=%s\nerr=%v", name, err))
		self.Error(fmt.Sprintf("Failed to create factorybin element for pad %s", name), err)
		return nil
	}
	screenShareIn.Queue, err = gst.NewElementWithProperties("queue", map[string]interface{}{
		"max-size-buffers": 3,
		"max-size-bytes":   0,
		"max-size-time":    0,
		"leaky":            2, // downstream
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create queue element for pad\npad=%s\nerr=%v", name, err))
		self.Error(fmt.Sprintf("Failed to create queue element for pad %s", name), err)
		return nil
	}

	if err := self.AddMany(screenShareIn.RTPVideo, screenShareIn.Queue); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add factorybin and queue elements to SIP IO element for pad\npad=%s\nerr=%v", name, err))
		self.Error(fmt.Sprintf("Failed to add factorybin and queue elements to SIP IO element for pad %s", name), err)
		return nil
	}

	if err := screenShareIn.RTPVideo.Link(screenShareIn.Queue); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link factorybin element to queue for pad\npad=%s\nerr=%v", name, err))
		self.Error(fmt.Sprintf("Failed to link factorybin element to queue for pad %s", name), err)
		return nil
	}

	screenShareIn.pad = e.Compositor.GetRequestPad(fmt.Sprintf("sink_%d_%d_%d", session, ssrc, pt))
	if screenShareIn.pad == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get request pad from compositor for pad\npad=%s", name))
		self.Error(fmt.Sprintf("Failed to get request pad from compositor for pad %s", name), fmt.Errorf("compositor returned nil pad"))
		return nil
	}

	if ret := screenShareIn.Queue.GetStaticPad("src").Link(screenShareIn.pad); ret != gst.PadLinkOK {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link queue src pad to compositor pad for pad\npad=%s\nret=%v", name, ret))
		self.Error(fmt.Sprintf("Failed to link queue src pad to compositor pad for pad %s", name), fmt.Errorf("failed to link pads"))
		return nil
	}

	screenShareIn.gpad = gst.NewGhostPadFromTemplate(name, screenShareIn.RTPVideo.GetStaticPad("sink"), templ)
	if screenShareIn.gpad == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create ghost pad for pad\npad=%s", name))
		self.Error(fmt.Sprintf("Failed to create ghost pad for pad %s", name), fmt.Errorf("gst.NewGhostPadFromTemplate returned nil"))
		return nil
	}
	if !screenShareIn.gpad.SetActive(true) {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to activate ghost pad for pad\npad=%s", name))
	}
	if !self.AddPad(screenShareIn.gpad.Pad) {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add ghost pad to SIP IO element for pad\npad=%s", name))
		self.Error(fmt.Sprintf("Failed to add ghost pad to SIP IO element for pad %s", name), fmt.Errorf("self.AddPad returned false"))
		return nil
	}

	if !screenShareIn.RTPVideo.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to sync state of factorybin element with parent for pad\npad=%s", name))
	}
	if !screenShareIn.Queue.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to sync state of queue element with parent for pad\npad=%s", name))
	}

	e.ScreenShareIn[name] = screenShareIn

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Successfully created new screen share input pad\npad=%s\nsession=%d", name, session))
	return screenShareIn.gpad.Pad
}

func (e *IoManagerLivekit) requestNewPadScreenShareAudioIn(self *gst.Bin, templ *gst.PadTemplate, name string, session int, ssrc int, pt int) *gst.Pad {
	e.inMu.Lock()
	defer e.inMu.Unlock()

	if _, exists := e.AudioIn[name]; exists {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Pad with name already exists\nname=%s", name))
		return nil
	}

	screenShareAudioIn := &ScreenShareAudioInTranscode{}

	var err error
	screenShareAudioIn.RtpAudio, err = gst.NewElementWithProperties("factorybin", map[string]interface{}{
		"factories": glib.NewStrv([]string{
			"g722-audio",
			"opus-audio",
			"pcmu-audio",
			"pcma-audio",
		}),
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create factorybin element for pad\npad=%s\nerr=%v", name, err))
		self.Error(fmt.Sprintf("Failed to create factorybin element for pad %s", name), err)
		return nil
	}
	screenShareAudioIn.Filter, err = gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString(AudioCaps),
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create capsfilter element for pad\npad=%s\nerr=%v", name, err))
		self.Error(fmt.Sprintf("Failed to create capsfilter element for pad %s", name), err)
		return nil
	}

	if err := self.AddMany(screenShareAudioIn.RtpAudio, screenShareAudioIn.Filter); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add factorybin element to SIP IO element for pad\npad=%s\nerr=%v", name, err))
		self.Error(fmt.Sprintf("Failed to add factorybin element to SIP IO element for pad %s", name), err)
		return nil
	}

	if err := screenShareAudioIn.RtpAudio.Link(screenShareAudioIn.Filter); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link factorybin element to capsfilter for pad\npad=%s\nerr=%v", name, err))
		self.Error(fmt.Sprintf("Failed to link factorybin element to capsfilter for pad %s", name), err)
		return nil
	}

	screenShareAudioIn.pad = e.Compositor.GetRequestPad(fmt.Sprintf("sink_%d_%d_%d", session, ssrc, pt))
	if screenShareAudioIn.pad == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get request pad from compositor for pad\npad=%s", name))
		self.Error(fmt.Sprintf("Failed to get request pad from compositor for pad %s", name), fmt.Errorf("compositor returned nil pad"))
		return nil
	}

	if ret := screenShareAudioIn.Filter.GetStaticPad("src").Link(screenShareAudioIn.pad); ret != gst.PadLinkOK {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link capsfilter src pad to compositor pad for pad\npad=%s\nret=%v", name, ret))
		self.Error(fmt.Sprintf("Failed to link capsfilter src pad to compositor pad for pad %s", name), fmt.Errorf("failed to link pads"))
		return nil
	}

	screenShareAudioIn.gpad = gst.NewGhostPadFromTemplate(name, screenShareAudioIn.RtpAudio.GetStaticPad("sink"), templ)
	if screenShareAudioIn.gpad == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create ghost pad for pad\npad=%s", name))
		self.Error(fmt.Sprintf("Failed to create ghost pad for pad %s", name), fmt.Errorf("gst.NewGhostPadFromTemplate returned nil"))
		return nil
	}
	if !screenShareAudioIn.gpad.SetActive(true) {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to activate ghost pad for pad\npad=%s", name))
	}
	if !self.AddPad(screenShareAudioIn.gpad.Pad) {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add ghost pad to SIP IO element for pad\npad=%s", name))
		self.Error(fmt.Sprintf("Failed to add ghost pad to SIP IO element for pad %s", name), fmt.Errorf("self.AddPad returned false"))
		return nil
	}

	if !screenShareAudioIn.RtpAudio.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to sync state of factorybin element with parent for pad\npad=%s", name))
	}
	if !screenShareAudioIn.Filter.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to sync state of capsfilter element with parent for pad\npad=%s", name))
	}

	e.ScreenShareAudioIn[name] = screenShareAudioIn

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Successfully created new screen share audio input pad\npad=%s\nsession=%d", name, session))
	return screenShareAudioIn.gpad.Pad
}

func (e *IoManagerLivekit) ReleasePad(instance *gst.Element, pad *gst.Pad) {
	self := gst.ToGstBin(instance)

	pname := pad.GetName()

	gpad := pad.AsGhostPad()
	if gpad == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Pad is not a ghost pad, cannot release\npad=%s", pname))
		return
	}

	templ := gpad.Template()
	if templ == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Ghost pad has no template, cannot release\npad=%s", pname))
		return
	}

	switch templ.Name() {
	case "recv_rtp_sink_%u_%u_%u":
		var session, ssrc, pt int
		if _, err := fmt.Sscanf(pname, "recv_rtp_sink_%d_%d_%d", &session, &ssrc, &pt); err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to parse pad name\npad=%s\nerr=%v", pname, err))
			return
		}

		switch livekit.TrackSource(session) {
		case livekit.TrackSource_MICROPHONE:
			e.releasePadAudioIn(self, gpad, pname, session, ssrc, pt)
		case livekit.TrackSource_CAMERA:
			e.releasePadCameraIn(self, gpad, pname, session, ssrc, pt)
		case livekit.TrackSource_SCREEN_SHARE:
			e.releasePadScreenShareIn(self, gpad, pname, session, ssrc, pt)
		case livekit.TrackSource_SCREEN_SHARE_AUDIO:
			e.releasePadScreenShareAudioIn(self, gpad, pname, session, ssrc, pt)
		default:
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Unsupported session kind in pad name\npad=%s\nsession=%d\nsource=%s", pname, session, livekit.TrackSource(session).String()))
			return
		}
	default:
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Unknown pad template for pad\ntemplate=%s\npad=%s", templ.Name(), pname))
		return
	}

	if !gpad.SetActive(false) {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to deactivate ghost pad\npad=%s", pname))
		return
	}
	if !self.RemovePad(gpad.Pad) {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to remove ghost pad from io_manager_livekit\npad=%s", pname))
		return
	}

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Successfully released pad\npad=%s", pname))
}

func (e *IoManagerLivekit) releasePadAudioIn(self *gst.Bin, _ *gst.GhostPad, pname string, session int, _ int, _ int) {
	e.inMu.Lock()
	defer e.inMu.Unlock()

	audioIn, exists := e.AudioIn[pname]
	if !exists {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("No audio input pad found with name\nname=%s", pname))
		return
	}

	if err := audioIn.RtpAudio.SetState(gst.StateNull); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set factorybin element to NULL state for pad\npad=%s\nerr=%v", pname, err))
	}
	if err := audioIn.Filter.SetState(gst.StateNull); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set capsfilter element to NULL state for pad\npad=%s\nerr=%v", pname, err))
	}

	e.Compositor.ReleaseRequestPad(audioIn.pad)

	if err := self.RemoveMany(audioIn.RtpAudio, audioIn.Filter); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to remove factorybin element from SIP IO element for pad\npad=%s\nerr=%v", pname, err))
	}

	delete(e.AudioIn, pname)

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Successfully released audio input pad\npad=%s\nsession=%d", pname, session))
}

func (e *IoManagerLivekit) releasePadCameraIn(self *gst.Bin, _ *gst.GhostPad, pname string, session int, _ int, _ int) {
	e.inMu.Lock()
	defer e.inMu.Unlock()

	cameraIn, exists := e.CameraIn[pname]
	if !exists {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("No camera input pad found with name\nname=%s", pname))
		return
	}

	if err := cameraIn.RTPVideo.SetState(gst.StateNull); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set factorybin element to NULL state for pad\npad=%s\nerr=%v", pname, err))
	}
	if err := cameraIn.Queue.SetState(gst.StateNull); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set queue element to NULL state for pad\npad=%s\nerr=%v", pname, err))
	}

	e.Compositor.ReleaseRequestPad(cameraIn.pad)

	if err := self.RemoveMany(cameraIn.RTPVideo, cameraIn.Queue); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to remove factorybin and queue element from SIP IO element for pad\npad=%s\nerr=%v", pname, err))
	}

	delete(e.CameraIn, pname)

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Successfully released camera input pad\npad=%s\nsession=%d", pname, session))
}

func (e *IoManagerLivekit) releasePadScreenShareIn(self *gst.Bin, _ *gst.GhostPad, pname string, session int, _ int, _ int) {
	e.inMu.Lock()
	defer e.inMu.Unlock()

	screenShareIn, exists := e.ScreenShareIn[pname]
	if !exists {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("No screen share input pad found with name\nname=%s", pname))
		return
	}

	if err := screenShareIn.RTPVideo.SetState(gst.StateNull); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set factorybin element to NULL state for pad\npad=%s\nerr=%v", pname, err))
	}
	if err := screenShareIn.Queue.SetState(gst.StateNull); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set queue element to NULL state for pad\npad=%s\nerr=%v", pname, err))
	}

	e.Compositor.ReleaseRequestPad(screenShareIn.pad)

	if err := self.RemoveMany(screenShareIn.RTPVideo, screenShareIn.Queue); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to remove factorybin and queue element from SIP IO element for pad\npad=%s\nerr=%v", pname, err))
	}

	delete(e.ScreenShareIn, pname)

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Successfully released screen share input pad\npad=%s\nsession=%d", pname, session))
}

func (e *IoManagerLivekit) releasePadScreenShareAudioIn(self *gst.Bin, _ *gst.GhostPad, pname string, session int, _ int, _ int) {
	e.inMu.Lock()
	defer e.inMu.Unlock()

	screenShareAudioIn, exists := e.ScreenShareAudioIn[pname]
	if !exists {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("No screen share audio input pad found with name\nname=%s", pname))
		return
	}

	if err := screenShareAudioIn.RtpAudio.SetState(gst.StateNull); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set factorybin element to NULL state for pad\npad=%s\nerr=%v", pname, err))
	}
	if err := screenShareAudioIn.Filter.SetState(gst.StateNull); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set capsfilter element to NULL state for pad\npad=%s\nerr=%v", pname, err))
	}

	e.Compositor.ReleaseRequestPad(screenShareAudioIn.pad)

	if err := self.RemoveMany(screenShareAudioIn.RtpAudio, screenShareAudioIn.Filter); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to remove factorybin element from SIP IO element for pad\npad=%s\nerr=%v", pname, err))
	}

	delete(e.ScreenShareAudioIn, pname)

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Successfully released screen share audio input pad\npad=%s\nsession=%d", pname, session))
}

func (e *IoManagerLivekit) compositorPadAdded(self *gst.Bin, pad *gst.Pad) {
	pname := pad.GetName()

	if !strings.HasPrefix(pname, "src_") {
		return
	}

	var session int
	if _, err := fmt.Sscanf(pname, "src_%d", &session); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to parse compositor pad name\npad=%s\nerr=%v", pname, err))
		return
	}

	switch livekit.TrackSource(session) {
	case livekit.TrackSource_MICROPHONE:
		e.padAddedAudioOut(self, pad, pname)
	case livekit.TrackSource_CAMERA:
		e.padAddedCameraOut(self, pad, pname)
	case livekit.TrackSource_SCREEN_SHARE:
		e.padAddedScreenShareOut(self, pad, pname)
	default:
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Unsupported session kind in compositor pad name\npad=%s\nsession=%d\nsource=%s", pname, session, livekit.TrackSource(session).String()))
	}
}

func (e *IoManagerLivekit) padAddedAudioOut(self *gst.Bin, pad *gst.Pad, name string) {
	e.outMu.Lock()
	defer e.outMu.Unlock()

	if e.AudioOut != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Audio output pad already exists, cannot add new pad\npad=%s", pad.GetName()))
		return
	}

	audioOut := &AudioOutTranscode{}

	var err error
	audioOut.Queue, err = gst.NewElementWithProperties("queue", map[string]interface{}{
		"max-size-buffers": uint(0),
		"max-size-bytes":   uint(0),
		"max-size-time":    uint(2_000_000_000), // 2 seconds
		"leaky":            int(2),              // downstream
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create queue element for audio output pad\nerr=%v", err))
		self.Error("Failed to create queue element for audio output pad", err)
		return
	}

	audioOut.AudioRtp, err = gst.NewElementWithProperties("factorybin", map[string]interface{}{
		"factories": glib.NewStrv([]string{
			"audio-g722",
			"audio-opus",
			"audio-pcmu",
			"audio-pcma",
		}),
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create factorybin element for audio output pad\nerr=%v", err))
		self.Error("Failed to create factorybin element for audio output pad", err)
		return
	}
	if err := self.AddMany(audioOut.Queue, audioOut.AudioRtp); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add factorybin element to SIP IO element for audio output pad\nerr=%v", err))
		self.Error("Failed to add factorybin element to SIP IO element for audio output pad", err)
		return
	}

	if err := audioOut.Queue.Link(audioOut.AudioRtp); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link queue element to factorybin element for audio output pad\nerr=%v", err))
		self.Error("Failed to link queue element to factorybin element for audio output pad", err)
		return
	}

	audioOut.pad = pad

	class := gst.ToElementClass(self.Class())

	if ret := audioOut.pad.Link(audioOut.Queue.GetStaticPad("sink")); ret != gst.PadLinkOK {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link audio output pad to factorybin sink pad\nret=%v", ret))
		self.Error("Failed to link audio output pad to factorybin sink pad", fmt.Errorf("failed to link pads"))
		return
	}

	audioOut.gpad = gst.NewGhostPadFromTemplate(fmt.Sprintf("send_rtp_src_%d", livekit.TrackSource_MICROPHONE), audioOut.AudioRtp.GetStaticPad("src"), class.GetPadTemplate("send_rtp_src_%u"))
	if audioOut.gpad == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create ghost pad for audio output pad\npad=%s", name))
		self.Error(fmt.Sprintf("Failed to create ghost pad for audio output pad %s", name), fmt.Errorf("gst.NewGhostPadFromTemplate returned nil"))
		return
	}
	if !audioOut.gpad.SetActive(true) {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to activate ghost pad for audio output pad\npad=%s", name))
	}
	if !self.AddPad(audioOut.gpad.Pad) {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add ghost pad to SIP IO element for audio output pad\npad=%s", name))
		self.Error(fmt.Sprintf("Failed to add ghost pad to SIP IO element for audio output pad %s", name), fmt.Errorf("self.AddPad returned false"))
		return
	}

	if !audioOut.AudioRtp.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, "Failed to sync state of factorybin element with parent")
	}
	if !audioOut.Queue.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, "Failed to sync state of queue element with parent")
	}

	e.AudioOut = audioOut

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Successfully added audio output pad\npad=%s", pad.GetName()))
}

func (e *IoManagerLivekit) padAddedCameraOut(self *gst.Bin, pad *gst.Pad, name string) {
	e.outMu.Lock()
	defer e.outMu.Unlock()

	if e.CameraOut != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Camera output pad already exists, cannot add new pad\npad=%s", pad.GetName()))
		return
	}

	cameraOut := &CameraOutTranscode{}

	var err error

	cameraOut.Queue, err = gst.NewElementWithProperties("queue", map[string]interface{}{
		"max-size-buffers": 3,
		"max-size-bytes":   0,
		"max-size-time":    0,
		"leaky":            2, // downstream
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create queue element for camera output pad\nerr=%v", err))
		self.Error("Failed to create queue element for camera output pad", err)
		return
	}

	properties := gst.NewStructure("properties")
	if err := errors.Join(
		properties.SetUint("*.video-width", e.videoWidth),
		properties.SetUint("*.video-height", e.videoHeight),
	); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to set properties for factorybin element for camera output pad\nerr=%v", err))
		self.Error("Failed to set properties for factorybin element for camera output pad", err)
		return
	}
	cameraOut.VideoRTP, err = gst.NewElementWithProperties("factorybin", map[string]interface{}{
		"factories": glib.NewStrv([]string{
			"video-vp9",
			"video-vp8",
			"video-h264",
		}),
		"child-properties": properties,
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create factorybin element for camera output pad\nerr=%v", err))
		self.Error("Failed to create factorybin element for camera output pad", err)
		return
	}

	if err := self.AddMany(cameraOut.Queue, cameraOut.VideoRTP); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add queue and factorybin elements to SIP IO element for camera output pad\nerr=%v", err))
		self.Error("Failed to add queue and factorybin elements to SIP IO element for camera output pad", err)
		return
	}

	if err := cameraOut.Queue.Link(cameraOut.VideoRTP); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link queue element to factorybin element for camera output pad\nerr=%v", err))
		self.Error("Failed to link queue element to factorybin element for camera output pad", err)
		return
	}

	cameraOut.pad = pad

	class := gst.ToElementClass(self.Class())

	if ret := cameraOut.pad.Link(cameraOut.Queue.GetStaticPad("sink")); ret != gst.PadLinkOK {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link camera output pad to queue sink pad\nret=%v", ret))
		self.Error("Failed to link camera output pad to queue sink pad", fmt.Errorf("failed to link pads"))
		return
	}

	cameraOut.gpad = gst.NewGhostPadFromTemplate(fmt.Sprintf("send_rtp_src_%d", livekit.TrackSource_CAMERA), cameraOut.VideoRTP.GetStaticPad("src"), class.GetPadTemplate("send_rtp_src_%u"))
	if cameraOut.gpad == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create ghost pad for camera output pad\npad=%s", name))
		self.Error(fmt.Sprintf("Failed to create ghost pad for camera output pad %s", name), fmt.Errorf("gst.NewGhostPadFromTemplate returned nil"))
		return
	}
	if !cameraOut.gpad.SetActive(true) {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to activate ghost pad for camera output pad\npad=%s", name))
	}
	if !self.AddPad(cameraOut.gpad.Pad) {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add ghost pad to SIP IO element for camera output pad\npad=%s", name))
		self.Error(fmt.Sprintf("Failed to add ghost pad to SIP IO element for camera output pad %s", name), fmt.Errorf("self.AddPad returned false"))
		return
	}

	if !cameraOut.Queue.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, "Failed to sync state of queue element with parent")
	}
	if !cameraOut.VideoRTP.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, "Failed to sync state of factorybin element with parent")
	}

	e.CameraOut = cameraOut

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Successfully added camera output pad\npad=%s", pad.GetName()))
}

func (e *IoManagerLivekit) padAddedScreenShareOut(self *gst.Bin, pad *gst.Pad, name string) {
	e.outMu.Lock()
	defer e.outMu.Unlock()

	if e.ScreenShareOut != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Screen share output pad already exists, cannot add new pad\npad=%s", pad.GetName()))
		return
	}

	screenShareOut := &ScreenShareOutTranscode{}

	var err error
	screenShareOut.Queue, err = gst.NewElementWithProperties("queue", map[string]interface{}{
		"max-size-buffers": 3,
		"max-size-bytes":   0,
		"max-size-time":    0,
		"leaky":            2, // downstream
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create queue element for screen share output pad\nerr=%v", err))
		self.Error("Failed to create queue element for screen share output pad", err)
		return
	}

	properties := gst.NewStructure("properties")
	if err := errors.Join(
		properties.SetUint("*.video-width", e.videoWidth),
		properties.SetUint("*.video-height", e.videoHeight),
	); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to set properties for factorybin element for screen share output pad\nerr=%v", err))
		self.Error("Failed to set properties for factorybin element for screen share output pad", err)
		return
	}
	screenShareOut.VideoRTP, err = gst.NewElementWithProperties("factorybin", map[string]interface{}{
		"factories": glib.NewStrv([]string{
			"video-vp9",
			"video-vp8",
			"video-h264",
		}),
		"child-properties": properties,
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create factorybin element for screen share output pad\nerr=%v", err))
		self.Error("Failed to create factorybin element for screen share output pad", err)
		return
	}
	if err := self.AddMany(screenShareOut.Queue, screenShareOut.VideoRTP); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add elements to SIP IO element for screen share output pad\nerr=%v", err))
		self.Error("Failed to add elements to SIP IO element for screen share output pad", err)
		return
	}

	if err := screenShareOut.Queue.Link(screenShareOut.VideoRTP); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link queue element to factorybin element for screen share output pad\nerr=%v", err))
		self.Error("Failed to link queue element to factorybin element for screen share output pad", err)
		return
	}

	screenShareOut.pad = pad

	class := gst.ToElementClass(self.Class())

	if ret := screenShareOut.pad.Link(screenShareOut.Queue.GetStaticPad("sink")); ret != gst.PadLinkOK {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link screen share output pad to queue sink pad\nret=%v", ret))
		self.Error("Failed to link screen share output pad to queue sink pad", fmt.Errorf("failed to link pads"))
		return
	}

	screenShareOut.gpad = gst.NewGhostPadFromTemplate(fmt.Sprintf("send_rtp_src_%d", livekit.TrackSource_SCREEN_SHARE), screenShareOut.VideoRTP.GetStaticPad("src"), class.GetPadTemplate("send_rtp_src_%u"))
	if screenShareOut.gpad == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create ghost pad for screen share output pad\npad=%s", name))
		self.Error(fmt.Sprintf("Failed to create ghost pad for screen share output pad %s", name), fmt.Errorf("gst.NewGhostPadFromTemplate returned nil"))
		return
	}
	if !screenShareOut.gpad.SetActive(true) {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to activate ghost pad for screen share output pad\npad=%s", name))
	}
	if !self.AddPad(screenShareOut.gpad.Pad) {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add ghost pad to SIP IO element for screen share output pad\npad=%s", name))
		self.Error(fmt.Sprintf("Failed to add ghost pad to SIP IO element for screen share output pad %s", name), fmt.Errorf("self.AddPad returned false"))
		return
	}

	if !screenShareOut.Queue.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, "Failed to sync state of queue element with parent")
	}
	if !screenShareOut.VideoRTP.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, "Failed to sync state of factorybin element with parent")
	}

	e.ScreenShareOut = screenShareOut

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Successfully added screen share output pad\npad=%s", pad.GetName()))

	if _, err := self.Emit("has-screenshare", true); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to emit has-screenshare signal for compositor pad\npad=%s\nerr=%v", pad.GetName(), err))
	}
}

func (e *IoManagerLivekit) compositorPadRemoved(self *gst.Bin, pad *gst.Pad) {
	pname := pad.GetName()

	if !strings.HasPrefix(pname, "src_") {
		return
	}

	var session int
	if _, err := fmt.Sscanf(pname, "src_%d", &session); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to parse fallback pad name\npad=%s\nerr=%v", pname, err))
		return
	}

	switch livekit.TrackSource(session) {
	case livekit.TrackSource_MICROPHONE:
		e.padRemovedAudioOut(self, pad, pname)
	case livekit.TrackSource_CAMERA:
		e.padRemovedCameraOut(self, pad, pname)
	case livekit.TrackSource_SCREEN_SHARE:
		e.padRemovedScreenShareOut(self, pad, pname)
	default:
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Unsupported session kind in compositor pad name\npad=%s\nsession=%d\nsource=%s", pname, session, livekit.TrackSource(session).String()))
	}
}

func (e *IoManagerLivekit) padRemovedAudioOut(self *gst.Bin, pad *gst.Pad, name string) {
	e.outMu.Lock()
	defer e.outMu.Unlock()

	if e.AudioOut == nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("No audio output pad exists, cannot remove pad\npad=%s", pad.GetName()))
		return
	}

	if err := e.AudioOut.Queue.SetState(gst.StateNull); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set queue element to NULL state for pad\npad=%s\nerr=%v", name, err))
	}

	if err := e.AudioOut.AudioRtp.SetState(gst.StateNull); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set factorybin element to NULL state for pad\npad=%s\nerr=%v", name, err))
	}

	if err := self.RemoveMany(e.AudioOut.Queue, e.AudioOut.AudioRtp); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to remove elements from SIP IO element for pad\npad=%s\nerr=%v", name, err))
	}

	if !self.RemovePad(e.AudioOut.gpad.Pad) {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to remove ghost pad for audio output pad\npad=%s", name))
	}

	e.AudioOut = nil

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Removed audio output pad\npad=%s", pad.GetName()))
}

func (e *IoManagerLivekit) padRemovedCameraOut(self *gst.Bin, pad *gst.Pad, name string) {
	e.outMu.Lock()
	defer e.outMu.Unlock()

	if e.CameraOut == nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("No camera output pad exists, cannot remove pad\npad=%s", pad.GetName()))
		return
	}

	if err := e.CameraOut.Queue.SetState(gst.StateNull); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set queue element to NULL state for pad\npad=%s\nerr=%v", name, err))
	}
	if err := e.CameraOut.VideoRTP.SetState(gst.StateNull); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set factorybin element to NULL state for pad\npad=%s\nerr=%v", name, err))
	}

	if err := self.RemoveMany(e.CameraOut.Queue, e.CameraOut.VideoRTP); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to remove elements from SIP IO element for pad\npad=%s\nerr=%v", name, err))
	}

	if !self.RemovePad(e.CameraOut.gpad.Pad) {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to remove ghost pad for camera output pad\npad=%s", name))
	}

	e.CameraOut = nil

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Removed camera output pad\npad=%s", pad.GetName()))
}

func (e *IoManagerLivekit) padRemovedScreenShareOut(self *gst.Bin, pad *gst.Pad, name string) {
	e.outMu.Lock()
	defer e.outMu.Unlock()

	if e.ScreenShareOut == nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("No screen share output pad exists, cannot remove pad\npad=%s", pad.GetName()))
		return
	}

	if err := e.ScreenShareOut.Queue.SetState(gst.StateNull); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set queue element to NULL state for pad\npad=%s\nerr=%v", name, err))
	}
	if err := e.ScreenShareOut.VideoRTP.SetState(gst.StateNull); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set factorybin element to NULL state for pad\npad=%s\nerr=%v", name, err))
	}

	if err := self.RemoveMany(e.ScreenShareOut.Queue, e.ScreenShareOut.VideoRTP); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to remove elements from SIP IO element for pad\npad=%s\nerr=%v", name, err))
	}

	if !self.RemovePad(e.ScreenShareOut.gpad.Pad) {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to remove ghost pad for screen share output pad\npad=%s", name))
	}

	e.ScreenShareOut = nil

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Removed screen share output pad\npad=%s", pad.GetName()))

	if _, err := self.Emit("has-screenshare", false); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to emit has-screenshare signal for compositor pad\npad=%s\nerr=%v", pad.GetName(), err))
	}
}

func (e *IoManagerLivekit) playAudioFd(self *gst.Bin, fd int) bool {
	// return true
	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("playAudioFd creating flacsource\nfd=%d", fd))

	flacSrc, err := gst.NewElementWithProperties("flacsource", map[string]interface{}{
		"fd": fd,
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create flacsource\nerr=%v", err))
		self.Error("Failed to create flacsource", err)
		unix.Close(fd)
		return false
	}

	filter, err := gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString(AudioCaps),
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create capsfilter for flacsource\nerr=%v", err))
		self.Error("Failed to create capsfilter for flacsource", err)
		return false
	}

	if err := self.AddMany(flacSrc, filter); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add flacsource to io_manager_livekit\nerr=%v", err))
		return false
	}

	if err := flacSrc.Link(filter); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link flacsource to capsfilter\nerr=%v", err))
		self.Error("Failed to link flacsource to capsfilter", err)
		return false
	}

	compositorPad := e.Compositor.GetRequestPad("raw_sink_%u")
	if compositorPad == nil {
		self.Log(CAT, gst.LevelError, "Failed to get raw_sink request pad from compositor")
		return false
	}
	defer e.Compositor.ReleaseRequestPad(compositorPad)

	srcPad := filter.GetStaticPad("src")

	eosCh := make(chan struct{}, 1)
	srcPad.AddProbe(gst.PadProbeTypeEventDownstream, func(pad *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		evt := info.GetEvent()
		if evt != nil && evt.Type() == gst.EventTypeEOS {
			select {
			case eosCh <- struct{}{}:
			default:
			}
			return gst.PadProbeRemove
		}
		return gst.PadProbeOK
	})

	if ret := srcPad.Link(compositorPad); ret != gst.PadLinkOK {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link flacsource src to compositor pad\nret=%v", ret))
		return false
	}

	if !flacSrc.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, "Failed to sync flacsource state with parent")
	}
	if !filter.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, "Failed to sync capsfilter state with parent")
	}

	self.Log(CAT, gst.LevelDebug, "playFlacFd: waiting for EOS")

	select {
	case <-eosCh:
		self.Log(CAT, gst.LevelDebug, "playFlacFd: got EOS, tearing down")
	case <-time.After(30 * time.Second):
		self.Log(CAT, gst.LevelWarning, "playFlacFd: timed out waiting for EOS, tearing down anyway")
	}

	if err := flacSrc.SetState(gst.StateNull); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set flacsource to NULL\nerr=%v", err))
	}
	if err := filter.SetState(gst.StateNull); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set capsfilter to NULL\nerr=%v", err))
	}

	if err := self.RemoveMany(flacSrc, filter); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to remove elements from io_manager_livekit\nerr=%v", err))
	}

	self.Log(CAT, gst.LevelInfo, "Successfully played FLAC fd")
	return true
}
