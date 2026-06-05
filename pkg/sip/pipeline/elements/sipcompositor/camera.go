package sipcompositor

import (
	"fmt"

	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/livekit"
)

type SipCompositorCamera struct {
	InputSelector *gst.Element
	Filter        *gst.Element
	gpad          *gst.GhostPad
}

func (e *SipCompositor) initCamera(self *gst.Bin) error {
	if e.SipCompositorCamera != nil {
		return nil
	}

	self.Log(CAT, gst.LevelInfo, "Initializing camera compositor")
	e.SipCompositorCamera = &SipCompositorCamera{}

	var err error
	e.SipCompositorCamera.InputSelector, err = gst.NewElementWithProperties("input-selector", map[string]interface{}{})
	if err != nil {
		return err
	}

	e.SipCompositorCamera.Filter, err = gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString(fmt.Sprintf("video/x-raw, width=(int)%d, height=(int)%d, framerate=%d/1", e.videoWidth, e.videoHeight, e.videoFramerate)),
	})
	if err != nil {
		return err
	}

	if err := self.AddMany(e.SipCompositorCamera.InputSelector, e.SipCompositorCamera.Filter); err != nil {
		return fmt.Errorf("failed to add elements to bin: %w", err)
	}

	if err := e.SipCompositorCamera.InputSelector.Link(e.SipCompositorCamera.Filter); err != nil {
		return fmt.Errorf("failed to link input-selector and capsfilter: %w", err)
	}

	class := gst.ToElementClass(self.Class())
	gpad := gst.NewGhostPadFromTemplate(fmt.Sprintf("src_%d", livekit.TrackSource_CAMERA), e.SipCompositorCamera.Filter.GetStaticPad("src"), class.GetPadTemplate("src_%u"))
	if gpad == nil {
		return fmt.Errorf("failed to create ghost pad for camera source")
	}
	e.SipCompositorCamera.gpad = gpad
	if !gpad.SetActive(true) {
		return fmt.Errorf("failed to activate ghost pad for camera source")
	}
	if !self.AddPad(gpad.Pad) {
		return fmt.Errorf("failed to add ghost pad for camera source to bin")
	}

	if !e.SipCompositorCamera.InputSelector.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, "Failed to sync state of input-selector with parent")
	}
	if !e.SipCompositorCamera.Filter.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, "Failed to sync state of capsfilter with parent")
	}

	return nil
}

func (e *SipCompositor) requestNewCameraSinkPad(self *gst.Bin, templ *gst.PadTemplate, name string) *gst.Pad {
	if err := e.initCamera(self); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to initialize camera compositor: %v", err))
		return nil
	}

	sink := e.SipCompositorCamera.InputSelector.GetRequestPad("sink_%u")
	if sink == nil {
		self.Log(CAT, gst.LevelError, "Failed to get request pad from input-selector for new camera sink")
		return nil
	}
	if err := e.SipCompositorCamera.InputSelector.SetProperty("active-pad", sink); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set active-pad property on input-selector for new camera sink: %v", err))
	}

	gpad := gst.NewGhostPadFromTemplate(name, sink, templ)
	if gpad == nil {
		self.Log(CAT, gst.LevelError, "Failed to create ghost pad for camera sink")
		return nil
	}
	if !gpad.SetActive(true) {
		self.Log(CAT, gst.LevelError, "Failed to activate ghost pad for camera sink")
		return nil
	}
	if !self.AddPad(gpad.Pad) {
		self.Log(CAT, gst.LevelError, "Failed to add ghost pad for camera sink to bin")
		return nil
	}

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Created new camera sink pad %s", gpad.GetName()))

	return gpad.Pad
}

func (e *SipCompositor) releaseCameraSinkPad(self *gst.Bin, gpad *gst.GhostPad) {
	if e.SipCompositorCamera == nil {
		self.Log(CAT, gst.LevelWarning, "Attempted to release camera sink pad but camera compositor is not initialized")
		return
	}

	target := gpad.GetTarget()
	if target == nil {
		self.Log(CAT, gst.LevelWarning, "Attempted to release camera sink pad but it has no target")
		return
	}

	e.SipCompositorCamera.InputSelector.ReleaseRequestPad(target)
	if !self.RemovePad(gpad.Pad) {
		self.Log(CAT, gst.LevelWarning, "Failed to remove ghost pad for camera sink from bin")
		return
	}
	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Released camera sink pad %s", gpad.GetName()))

	sinks, err := e.SipCompositorCamera.InputSelector.GetSinkPads()
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get sink pads from input-selector: %v", err))
		return
	}
	if len(sinks) == 0 {
		self.Log(CAT, gst.LevelInfo, "No more active camera sink pads, disabling camera")
		e.cleanupCamera(self)
	} else {
		if err := e.SipCompositorCamera.InputSelector.SetProperty("active-pad", sinks[len(sinks)-1]); err != nil {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set active-pad property on input-selector after releasing camera sink pad: %v", err))
		}
	}
}

func (e *SipCompositor) cleanupCamera(self *gst.Bin) {
	if e.SipCompositorCamera == nil {
		return
	}

	if err := e.SipCompositorCamera.InputSelector.SetState(gst.StateNull); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set input-selector to null state after releasing last camera sink pad: %v", err))
	}
	if err := e.SipCompositorCamera.Filter.SetState(gst.StateNull); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set capsfilter to null state after releasing last camera sink pad: %v", err))
	}
	if err := self.RemoveMany(e.SipCompositorCamera.InputSelector, e.SipCompositorCamera.Filter); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to remove input-selector from bin after releasing last camera sink pad: %v", err))
	}
	if !self.RemovePad(e.SipCompositorCamera.gpad.Pad) {
		self.Log(CAT, gst.LevelWarning, "Failed to remove ghost pad for camera source from bin after releasing last camera sink pad")
	}

	e.SipCompositorCamera = nil
}
