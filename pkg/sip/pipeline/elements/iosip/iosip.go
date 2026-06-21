package iosip

import (
	"errors"
	"fmt"
	"strings"
	"weak"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/livekit"
)

func (e *IoManagerSip) RequestNewPad(instance *gst.Element, templ *gst.PadTemplate, name string, caps *gst.Caps) *gst.Pad {
	self := gst.ToGstBin(instance)

	var session, ssrc, pt int
	if _, err := fmt.Sscanf(name, "recv_rtp_sink_%d_%d_%d", &session, &ssrc, &pt); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to parse pad name\npad=%s\nerr=%v", name, err))
		return nil
	}

	if pt < 0 || pt > 127 {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Invalid payload type in pad name\npad=%s\npt=%d", name, pt))
		return nil
	}

	if session < 0 || session > int(livekit.TrackSource_SCREEN_SHARE_AUDIO) {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Invalid session kind in pad name\npad=%s\nsession=%d\nsource=%s", name, session, livekit.TrackSource(session).String()))
		return nil
	}

	switch livekit.TrackSource(session) {
	case livekit.TrackSource_MICROPHONE:
		return e.requestNewPadAudioIn(self, templ, name, session, ssrc, pt)
	case livekit.TrackSource_CAMERA:
		return e.requestNewPadCameraIn(self, templ, name, session, ssrc, pt)
	case livekit.TrackSource_SCREEN_SHARE:
		return e.requestNewPadScreenshareIn(self, templ, name, session, ssrc, pt)
	default:
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Unsupported session kind in pad name\npad=%s\nsession=%d\nsource=%s", name, session, livekit.TrackSource(session).String()))
		return nil
	}
}

func (e *IoManagerSip) requestNewPadAudioIn(self *gst.Bin, templ *gst.PadTemplate, name string, session int, ssrc int, pt int) *gst.Pad {
	e.inMu.Lock()
	defer e.inMu.Unlock()

	if _, exists := e.AudioIn[name]; exists {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Pad with name already exists\npad=%s", name))
		return nil
	}

	class := gst.ToElementClass(self.Class())
	gpad := gst.NewGhostPadNoTargetFromTemplate(name, class.GetPadTemplate("recv_rtp_sink_%u_%u_%u"))
	if gpad == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create ghost pad for pad\npad=%s", name))
		return nil
	}
	if !gpad.SetActive(true) {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to activate ghost pad for pad\npad=%s", name))
	}
	if !self.AddPad(gpad.Pad) {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add ghost pad to SIP IO element for pad\npad=%s", name))
		return nil
	}

	weakE := weak.Make(e)
	gpad.AddProbe(gst.PadProbeTypeEventDownstream, func(pad *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		ptr := weakE.Value()
		if ptr == nil {
			return gst.PadProbeRemove
		}
		return ptr.linkNewPadAudio(pad, info, name, session, ssrc, pt)
	})

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Successfully created new audio input pad\npad=%s\nsession=%d", name, session))
	return gpad.Pad
}

func (e *IoManagerSip) linkNewPadAudio(pad *gst.Pad, info *gst.PadProbeInfo, name string, session int, ssrc int, pt int) gst.PadProbeReturn {
	event := info.GetEvent()
	if event == nil || event.Type() != gst.EventTypeCaps {
		return gst.PadProbeOK
	}

	gpad := pad.AsGhostPad()
	self := gst.ToGstBin(gpad.GetParent())
	if self == nil || self.Instance() == nil {
		CAT.Log(gst.LevelError, "Failed to get SIP IO element from ghost pad parent when linking new audio pad")
		return gst.PadProbeRemove
	}

	caps := event.ParseCaps()
	if caps == nil {
		self.Log(CAT, gst.LevelError, "Failed to query caps from peer pad when linking new audio pad in SIP IO element")
		return gst.PadProbeRemove
	}

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Linking new audio pad in SIP IO element\npad=%s\ncaps=%s", gpad.GetName(), caps.String()))

	var err error
	defer func() {
		if err == nil {
			return
		}
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link new audio pad in SIP IO element\nerr=%v", err))
		self.Error("Failed to link new audio pad in SIP IO element", err)
	}()

	encVal, err := caps.GetStructureAt(0).GetValue("encoding-name")
	if err != nil {
		err = fmt.Errorf("Failed to get encoding name from caps: %w (%s)", err, caps.String())
		return gst.PadProbeRemove
	}

	enc, ok := encVal.(string)
	if !ok {
		err = fmt.Errorf("Encoding name in caps is not a string: %T (%s)", encVal, caps.String())
		return gst.PadProbeRemove
	}

	switch strings.ToLower(enc) {
	case "telephone-event":
		if err := e.linkNewPadAudioDtmf(self, pad, name, caps, session, ssrc, pt); err != nil {
			err = fmt.Errorf("Failed to link new audio pad for DTMF input: %w", err)
			return gst.PadProbeRemove
		}
	default:
		if err := e.linkNewPadAudioMicrophone(self, pad, name, caps, session, ssrc, pt); err != nil {
			err = fmt.Errorf("Failed to link new audio pad for microphone input: %w", err)
			return gst.PadProbeRemove
		}
	}
	return gst.PadProbeRemove
}

func (e *IoManagerSip) linkNewPadAudioMicrophone(self *gst.Bin, pad *gst.Pad, name string, caps *gst.Caps, session int, ssrc int, pt int) error {
	e.inMu.Lock()
	defer e.inMu.Unlock()

	gpad := pad.AsGhostPad()

	_, exists := e.AudioIn[name]
	if exists {
		return fmt.Errorf("Audio input pad %s already exists in map", name)
	}

	audioIn := &SipAudioInTranscode{}
	audioIn.gpad = gpad

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
		return fmt.Errorf("Failed to create factorybin element for pad %s: %w", name, err)
	}
	audioIn.DtmfDetect, err = gst.NewElementWithProperties("dtmfdetect", map[string]interface{}{})
	if err != nil {
		return fmt.Errorf("Failed to create dtmfdetect element for pad %s: %w", name, err)
	}

	if err := self.AddMany(audioIn.RtpAudio, audioIn.DtmfDetect); err != nil {
		return fmt.Errorf("Failed to add factorybin element to SIP IO element for pad %s: %w", name, err)
	}

	if ret := audioIn.RtpAudio.GetStaticPad("src").Link(audioIn.DtmfDetect.GetStaticPad("sink")); ret != gst.PadLinkOK {
		return fmt.Errorf("Failed to link factorybin src pad to dtmfdetect sink pad for pad %s: %v", name, ret)
	}

	audioIn.pad = e.Compositor.GetRequestPad(fmt.Sprintf("sink_%d_%d_%d", session, ssrc, pt))
	if audioIn.pad == nil {
		return fmt.Errorf("Failed to get request pad from compositor for pad %s", name)
	}

	if ret := audioIn.DtmfDetect.GetStaticPad("src").Link(audioIn.pad); ret != gst.PadLinkOK {
		return fmt.Errorf("Failed to link dtmfdetect src pad to compositor pad for pad %s: %v", name, ret)
	}

	if !gpad.SetTarget(audioIn.RtpAudio.GetStaticPad("sink")) {
		return fmt.Errorf("Failed to set target pad for ghost pad %s", name)
	}

	if !audioIn.RtpAudio.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to sync state of factorybin element with parent for pad\npad=%s", name))
	}
	if !audioIn.DtmfDetect.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to sync state of dtmfdetect element with parent for pad\npad=%s", name))
	}

	e.AudioIn[name] = audioIn

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Successfully linked audio pad with factorybin element\npad=%s", name))

	return nil
}

func (e *IoManagerSip) linkNewPadAudioDtmf(self *gst.Bin, pad *gst.Pad, name string, caps *gst.Caps, session int, ssrc int, pt int) error {
	e.inMu.Lock()
	defer e.inMu.Unlock()

	gpad := pad.AsGhostPad()

	_, exists := e.DtmfIn[name]
	if exists {
		return fmt.Errorf("Audio input pad %s already exists in map", name)
	}

	dtmfIn := &SipDtmfInTranscode{}
	dtmfIn.gpad = gpad

	var err error
	dtmfIn.RtpDtmfDepay, err = gst.NewElementWithProperties("rtpdtmfdepay", map[string]interface{}{})
	if err != nil {
		return fmt.Errorf("Failed to create rtpdtmfdepay element for pad %s: %w", name, err)
	}

	dtmfIn.FakeSink, err = gst.NewElementWithProperties("fakesink", map[string]interface{}{
		"sync": false,
	})
	if err != nil {
		return fmt.Errorf("Failed to create fakesink element for pad %s: %w", name, err)
	}

	if err := self.AddMany(dtmfIn.RtpDtmfDepay, dtmfIn.FakeSink); err != nil {
		return fmt.Errorf("Failed to add rtpdtmfdepay element to SIP IO element for pad %s: %w", name, err)
	}

	if ret := dtmfIn.RtpDtmfDepay.GetStaticPad("src").Link(dtmfIn.FakeSink.GetStaticPad("sink")); ret != gst.PadLinkOK {
		return fmt.Errorf("Failed to link rtpdtmfdepay src pad to fakesink pad for pad %s: %v", name, ret)
	}

	if !gpad.SetTarget(dtmfIn.RtpDtmfDepay.GetStaticPad("sink")) {
		return fmt.Errorf("Failed to set target pad for ghost pad %s", name)
	}

	if !dtmfIn.RtpDtmfDepay.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to sync state of rtpdtmfdepay element with parent for pad\npad=%s", name))
	}

	if !dtmfIn.FakeSink.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to sync state of fakesink element with parent for pad\npad=%s", name))
	}

	e.DtmfIn[name] = dtmfIn

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Successfully linked audio pad with rtpdtmfdepay element\npad=%s", name))

	return nil
}

func (e *IoManagerSip) requestNewPadCameraIn(self *gst.Bin, templ *gst.PadTemplate, name string, session int, ssrc int, pt int) *gst.Pad {
	e.inMu.Lock()
	defer e.inMu.Unlock()

	if _, exists := e.CameraIn[name]; exists {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Pad with name already exists\npad=%s", name))
		return nil
	}

	cameraIn := &SipCameraInTranscode{}

	var err error
	properties := gst.NewStructure("properties")
	if err := errors.Join(
		properties.SetUint("*.video-width", e.videoWidth),
		properties.SetUint("*.video-height", e.videoHeight),
	); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to set properties for factorybin element for camera input pad\npad=%s\nerr=%v", name, err))
		self.Error(fmt.Sprintf("Failed to set properties for factorybin element for camera input pad %s", name), err)
		return nil
	}
	cameraIn.RTPVideo, err = gst.NewElementWithProperties("factorybin", map[string]interface{}{
		"factories": glib.NewStrv([]string{
			"h264-video",
			"vp9-video",
			"vp8-video",
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

func (e *IoManagerSip) requestNewPadScreenshareIn(self *gst.Bin, templ *gst.PadTemplate, name string, session int, ssrc int, pt int) *gst.Pad {
	e.inMu.Lock()
	defer e.inMu.Unlock()

	if _, exists := e.ScreenshareIn[name]; exists {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Pad with name already exists\npad=%s", name))
		return nil
	}

	screenshareIn := &SipScreenshareInTranscode{}

	var err error
	properties := gst.NewStructure("properties")
	if err := errors.Join(
		properties.SetUint("*.video-width", e.videoWidth),
		properties.SetUint("*.video-height", e.videoHeight),
	); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to set properties for factorybin element for screenshare input pad\npad=%s\nerr=%v", name, err))
		self.Error(fmt.Sprintf("Failed to set properties for factorybin element for screenshare input pad %s", name), err)
		return nil
	}
	screenshareIn.RTPVideo, err = gst.NewElementWithProperties("factorybin", map[string]interface{}{
		"factories": glib.NewStrv([]string{
			"h264-video",
			"vp9-video",
			"vp8-video",
		}),
		"child-properties": properties,
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create factorybin element for pad\npad=%s\nerr=%v", name, err))
		self.Error(fmt.Sprintf("Failed to create factorybin element for pad %s", name), err)
		return nil
	}
	screenshareIn.Queue, err = gst.NewElementWithProperties("queue", map[string]interface{}{
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

	if err := self.AddMany(screenshareIn.RTPVideo, screenshareIn.Queue); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add factorybin and queue elements to SIP IO element for pad\npad=%s\nerr=%v", name, err))
		self.Error(fmt.Sprintf("Failed to add factorybin and queue elements to SIP IO element for pad %s", name), err)
		return nil
	}

	if err := screenshareIn.RTPVideo.Link(screenshareIn.Queue); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link factorybin element to queue for pad\npad=%s\nerr=%v", name, err))
		self.Error(fmt.Sprintf("Failed to link factorybin element to queue for pad %s", name), err)
		return nil
	}

	screenshareIn.pad = e.Compositor.GetRequestPad(fmt.Sprintf("sink_%d_%d_%d", session, ssrc, pt))
	if screenshareIn.pad == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get request pad from compositor for pad\npad=%s", name))
		self.Error(fmt.Sprintf("Failed to get request pad from compositor for pad %s", name), fmt.Errorf("compositor returned nil pad"))
		return nil
	}

	if ret := screenshareIn.Queue.GetStaticPad("src").Link(screenshareIn.pad); ret != gst.PadLinkOK {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link queue src pad to compositor pad for pad\npad=%s\nret=%v", name, ret))
		self.Error(fmt.Sprintf("Failed to link queue src pad to compositor pad for pad %s", name), fmt.Errorf("failed to link pads"))
		return nil
	}

	screenshareIn.gpad = gst.NewGhostPadFromTemplate(name, screenshareIn.RTPVideo.GetStaticPad("sink"), templ)
	if screenshareIn.gpad == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create ghost pad for pad\npad=%s", name))
		self.Error(fmt.Sprintf("Failed to create ghost pad for pad %s", name), fmt.Errorf("gst.NewGhostPadFromTemplate returned nil"))
		return nil
	}
	if !screenshareIn.gpad.SetActive(true) {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to activate ghost pad for pad\npad=%s", name))
	}
	if !self.AddPad(screenshareIn.gpad.Pad) {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add ghost pad to SIP IO element for pad\npad=%s", name))
		self.Error(fmt.Sprintf("Failed to add ghost pad to SIP IO element for pad %s", name), fmt.Errorf("self.AddPad returned false"))
		return nil
	}

	if !screenshareIn.RTPVideo.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to sync state of factorybin element with parent for pad\npad=%s", name))
	}
	if !screenshareIn.Queue.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to sync state of queue element with parent for pad\npad=%s", name))
	}

	e.ScreenshareIn[name] = screenshareIn

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Successfully created new screenshare input pad\npad=%s\nsession=%d", name, session))
	return screenshareIn.gpad.Pad
}

func (e *IoManagerSip) ReleasePad(instance *gst.Element, pad *gst.Pad) {
	self := gst.ToGstBin(instance)

	pname := pad.GetName()
	if !strings.HasPrefix(pname, "recv_rtp_sink_") {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Invalid pad name, expected to start with recv_rtp_sink_\npad=%s", pname))
		return
	}

	gpad := pad.AsGhostPad()
	if gpad == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Pad is not a ghost pad, cannot release\npad=%s", pname))
		return
	}

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
		e.releasePadScreenshareIn(self, gpad, pname, session, ssrc, pt)
	default:
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Unsupported session kind in pad name\npad=%s\nsession=%d\nsource=%s", pname, session, livekit.TrackSource(session).String()))
		return
	}

	if !gpad.SetActive(false) {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to deactivate ghost pad\npad=%s", pname))
		return
	}
	if !self.RemovePad(gpad.Pad) {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to remove ghost pad from SIP IO element\npad=%s", pname))
		return
	}

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Successfully released pad\npad=%s\nsession=%d", pname, session))
}

func (e *IoManagerSip) releasePadAudioIn(self *gst.Bin, _ *gst.GhostPad, pname string, session int, _ int, _ int) {
	e.inMu.Lock()
	defer e.inMu.Unlock()

	audioIn, exists := e.AudioIn[pname]
	if !exists {
		if dtmfIn, exists := e.DtmfIn[pname]; exists {
			e.releasePadAudioDtmf(self, dtmfIn, pname)
			return
		}
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("No audio input pad found with name\npad=%s", pname))
		return
	}

	if audioIn.RtpAudio != nil {
		if err := audioIn.RtpAudio.SetState(gst.StateNull); err != nil {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set decoder element to NULL state for pad\npad=%s\nerr=%v", pname, err))
		}
		if audioIn.DtmfDetect != nil {
			if err := audioIn.DtmfDetect.SetState(gst.StateNull); err != nil {
				self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set dtmfdetect element to NULL state for pad\npad=%s\nerr=%v", pname, err))
			}
		}

		if audioIn.pad != nil {
			e.Compositor.ReleaseRequestPad(audioIn.pad)
		}

		if err := self.Remove(audioIn.RtpAudio); err != nil {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to remove decoder element from SIP IO element for pad\npad=%s\nerr=%v", pname, err))
		}
		if audioIn.DtmfDetect != nil {
			if err := self.Remove(audioIn.DtmfDetect); err != nil {
				self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to remove dtmfdetect element from SIP IO element for pad\npad=%s\nerr=%v", pname, err))
			}
		}
	}

	delete(e.AudioIn, pname)

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Successfully released audio input pad\npad=%s\nsession=%d", pname, session))
}

func (e *IoManagerSip) releasePadAudioDtmf(self *gst.Bin, dtmfIn *SipDtmfInTranscode, pname string) {
	for _, element := range []*gst.Element{dtmfIn.RtpDtmfDepay, dtmfIn.FakeSink} {
		if element != nil {
			if err := element.SetState(gst.StateNull); err != nil {
				self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set element to NULL state for pad\nname=%s\npad=%s\nerr=%v", element.GetName(), pname, err))
			}
			if err := self.Remove(element); err != nil {
				self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to remove element from SIP IO element for pad\nname=%s\npad=%s\nerr=%v", element.GetName(), pname, err))
			}
		}
	}

	delete(e.DtmfIn, pname)

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Successfully released DTMF input pad\npad=%s", pname))
}

func (e *IoManagerSip) releasePadCameraIn(self *gst.Bin, _ *gst.GhostPad, pname string, session int, _ int, _ int) {
	e.inMu.Lock()
	defer e.inMu.Unlock()

	cameraIn, exists := e.CameraIn[pname]
	if !exists {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("No camera input pad found with name\npad=%s", pname))
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

func (e *IoManagerSip) releasePadScreenshareIn(self *gst.Bin, _ *gst.GhostPad, pname string, session int, _ int, _ int) {
	e.inMu.Lock()
	defer e.inMu.Unlock()

	screenshareIn, exists := e.ScreenshareIn[pname]
	if !exists {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("No screenshare input pad found with name\npad=%s", pname))
		return
	}

	if err := screenshareIn.RTPVideo.SetState(gst.StateNull); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set factorybin element to NULL state for pad\npad=%s\nerr=%v", pname, err))
	}
	if err := screenshareIn.Queue.SetState(gst.StateNull); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set queue element to NULL state for pad\npad=%s\nerr=%v", pname, err))
	}

	e.Compositor.ReleaseRequestPad(screenshareIn.pad)

	if err := self.RemoveMany(screenshareIn.RTPVideo, screenshareIn.Queue); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to remove factorybin and queue element from SIP IO element for pad\npad=%s\nerr=%v", pname, err))
	}

	delete(e.ScreenshareIn, pname)

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Successfully released screenshare input pad\npad=%s\nsession=%d", pname, session))
}

func (e *IoManagerSip) compositorPadAdded(self *gst.Bin, pad *gst.Pad) {
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
		e.padAddedScreenshareOut(self, pad, pname)
	default:
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Unsupported session kind in compositor pad name\npad=%s\nsession=%d\nsource=%s", pname, session, livekit.TrackSource(session).String()))
	}
}

func (e *IoManagerSip) padAddedAudioOut(self *gst.Bin, pad *gst.Pad, name string) {
	e.outMu.Lock()
	defer e.outMu.Unlock()

	if e.AudioOut != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Audio output pad already exists, cannot add new pad\npad=%s", pad.GetName()))
		return
	}

	audioOut := &SipAudioOutTranscode{}

	var err error
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
	if err := self.Add(audioOut.AudioRtp); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add factorybin element to SIP IO element for audio output pad\nerr=%v", err))
		self.Error("Failed to add factorybin element to SIP IO element for audio output pad", err)
		return
	}

	audioOut.pad = pad

	class := gst.ToElementClass(self.Class())

	if ret := audioOut.pad.Link(audioOut.AudioRtp.GetStaticPad("sink")); ret != gst.PadLinkOK {
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

	e.AudioOut = audioOut

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Successfully added audio output pad\npad=%s", pad.GetName()))
}

func (e *IoManagerSip) padAddedCameraOut(self *gst.Bin, pad *gst.Pad, name string) {
	e.outMu.Lock()
	defer e.outMu.Unlock()

	if e.CameraOut != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Camera output pad already exists, cannot add new pad\npad=%s", pad.GetName()))
		return
	}

	cameraOut := &SipCameraOutTranscode{}

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
			"video-h264",
			"video-vp9",
			"video-vp8",
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

func (e *IoManagerSip) padAddedScreenshareOut(self *gst.Bin, pad *gst.Pad, name string) {
	e.outMu.Lock()
	defer e.outMu.Unlock()

	if e.ScreenshareOut != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Screenshare output pad already exists, cannot add new pad\npad=%s", pad.GetName()))
		return
	}

	screenshareOut := &SipScreenshareOutTranscode{}

	var err error
	screenshareOut.Queue, err = gst.NewElementWithProperties("queue", map[string]interface{}{
		"max-size-buffers": 3,
		"max-size-bytes":   0,
		"max-size-time":    0,
		"leaky":            2, // downstream
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create queue element for screenshare output pad\nerr=%v", err))
		self.Error("Failed to create queue element for screenshare output pad", err)
		return
	}

	properties := gst.NewStructure("properties")
	if err := errors.Join(
		properties.SetUint("*.video-width", e.videoWidth),
		properties.SetUint("*.video-height", e.videoHeight),
	); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to set properties for factorybin element for screenshare output pad\nerr=%v", err))
		self.Error("Failed to set properties for factorybin element for screenshare output pad", err)
		return
	}
	screenshareOut.VideoRTP, err = gst.NewElementWithProperties("factorybin", map[string]interface{}{
		"factories": glib.NewStrv([]string{
			"video-h264",
			"video-vp9",
			"video-vp8",
		}),
		"child-properties": properties,
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create factorybin element for screenshare output pad\nerr=%v", err))
		self.Error("Failed to create factorybin element for screenshare output pad", err)
		return
	}
	if err := self.AddMany(screenshareOut.Queue, screenshareOut.VideoRTP); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add elements to SIP IO element for screenshare output pad\nerr=%v", err))
		self.Error("Failed to add elements to SIP IO element for screenshare output pad", err)
		return
	}

	if err := screenshareOut.Queue.Link(screenshareOut.VideoRTP); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link queue element to factorybin element for screenshare output pad\nerr=%v", err))
		self.Error("Failed to link queue element to factorybin element for screenshare output pad", err)
		return
	}

	screenshareOut.pad = pad

	class := gst.ToElementClass(self.Class())

	if ret := screenshareOut.pad.Link(screenshareOut.Queue.GetStaticPad("sink")); ret != gst.PadLinkOK {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link screenshare output pad to queue sink pad\nret=%v", ret))
		self.Error("Failed to link screenshare output pad to queue sink pad", fmt.Errorf("failed to link pads"))
		return
	}

	screenshareOut.gpad = gst.NewGhostPadFromTemplate(fmt.Sprintf("send_rtp_src_%d", livekit.TrackSource_SCREEN_SHARE), screenshareOut.VideoRTP.GetStaticPad("src"), class.GetPadTemplate("send_rtp_src_%u"))
	if screenshareOut.gpad == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create ghost pad for screenshare output pad\npad=%s", name))
		self.Error(fmt.Sprintf("Failed to create ghost pad for screenshare output pad %s", name), fmt.Errorf("gst.NewGhostPadFromTemplate returned nil"))
		return
	}
	if !screenshareOut.gpad.SetActive(true) {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to activate ghost pad for screenshare output pad\npad=%s", name))
	}
	if !self.AddPad(screenshareOut.gpad.Pad) {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add ghost pad to SIP IO element for screenshare output pad\npad=%s", name))
		self.Error(fmt.Sprintf("Failed to add ghost pad to SIP IO element for screenshare output pad %s", name), fmt.Errorf("self.AddPad returned false"))
		return
	}

	if !screenshareOut.Queue.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, "Failed to sync state of queue element with parent")
	}
	if !screenshareOut.VideoRTP.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, "Failed to sync state of factorybin element with parent")
	}

	e.ScreenshareOut = screenshareOut

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Successfully added screenshare output pad\npad=%s", pad.GetName()))
}

func (e *IoManagerSip) compositorPadRemoved(self *gst.Bin, pad *gst.Pad) {
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
		e.padRemovedAudioOut(self, pad, pname)
	case livekit.TrackSource_CAMERA:
		e.padRemovedCameraOut(self, pad, pname)
	case livekit.TrackSource_SCREEN_SHARE:
		e.padRemovedScreenshareOut(self, pad, pname)
	default:
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Unsupported session kind in compositor pad name\npad=%s\nsession=%d\nsource=%s", pname, session, livekit.TrackSource(session).String()))
	}
}

func (e *IoManagerSip) padRemovedAudioOut(self *gst.Bin, pad *gst.Pad, name string) {
	e.outMu.Lock()
	defer e.outMu.Unlock()

	if e.AudioOut == nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("No audio output pad exists, cannot remove pad\npad=%s", pad.GetName()))
		return
	}

	if err := e.AudioOut.AudioRtp.SetState(gst.StateNull); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set factorybin element to NULL state for pad\npad=%s\nerr=%v", name, err))
	}

	if err := self.Remove(e.AudioOut.AudioRtp); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to remove factorybin element from SIP IO element for pad\npad=%s\nerr=%v", name, err))
	}

	if !self.RemovePad(e.AudioOut.gpad.Pad) {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to remove ghost pad for audio output pad\npad=%s", name))
	}

	e.AudioOut = nil

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Removed audio output pad\npad=%s", pad.GetName()))
}

func (e *IoManagerSip) padRemovedCameraOut(self *gst.Bin, pad *gst.Pad, name string) {
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

func (e *IoManagerSip) padRemovedScreenshareOut(self *gst.Bin, pad *gst.Pad, name string) {
	e.outMu.Lock()
	defer e.outMu.Unlock()

	if e.ScreenshareOut == nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("No screenshare output pad exists, cannot remove pad\npad=%s", pad.GetName()))
		return
	}

	if err := e.ScreenshareOut.Queue.SetState(gst.StateNull); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set queue element to NULL state for pad\npad=%s\nerr=%v", name, err))
	}
	if err := e.ScreenshareOut.VideoRTP.SetState(gst.StateNull); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set factorybin element to NULL state for pad\npad=%s\nerr=%v", name, err))
	}

	if err := self.RemoveMany(e.ScreenshareOut.Queue, e.ScreenshareOut.VideoRTP); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to remove elements from SIP IO element for pad\npad=%s\nerr=%v", name, err))
	}

	if !self.RemovePad(e.ScreenshareOut.gpad.Pad) {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to remove ghost pad for screenshare output pad\npad=%s", name))
	}

	e.ScreenshareOut = nil

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Removed screenshare output pad\npad=%s", pad.GetName()))
}
