package sipcompositor

import (
	"fmt"

	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/livekit"
)

type SipCompositorMicrophone struct {
	InputSelector *gst.Element
}

func (e *SipCompositor) initMicrophone(self *gst.Bin) error {
	if e.SipCompositorMicrophone != nil {
		return nil
	}

	self.Log(CAT, gst.LevelInfo, "Initializing microphone compositor")
	e.SipCompositorMicrophone = &SipCompositorMicrophone{}

	var err error
	e.SipCompositorMicrophone.InputSelector, err = gst.NewElementWithProperties("input-selector", map[string]interface{}{})
	if err != nil {
		return err
	}

	if err := self.Add(e.SipCompositorMicrophone.InputSelector); err != nil {
		return fmt.Errorf("failed to add input-selector to bin: %w", err)
	}

	class := gst.ToElementClass(self.Class())
	gpad := gst.NewGhostPadFromTemplate(fmt.Sprintf("src_%d", livekit.TrackSource_MICROPHONE), e.SipCompositorMicrophone.InputSelector.GetStaticPad("src"), class.GetPadTemplate("src_%u"))
	if gpad == nil {
		return fmt.Errorf("failed to create ghost pad for microphone source")
	}
	if !gpad.SetActive(true) {
		return fmt.Errorf("failed to activate ghost pad for microphone source")
	}
	if !self.AddPad(gpad.Pad) {
		return fmt.Errorf("failed to add ghost pad for microphone source to bin")
	}

	if !e.SipCompositorMicrophone.InputSelector.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, "Failed to sync state of input-selector with parent")
	}

	return nil
}

func (e *SipCompositor) requestNewMicrophoneSinkPad(self *gst.Bin, templ *gst.PadTemplate, name string) *gst.Pad {
	if err := e.initMicrophone(self); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to initialize microphone compositor: %v", err))
		return nil
	}

	sink := e.SipCompositorMicrophone.InputSelector.GetRequestPad("sink_%u")
	if sink == nil {
		self.Log(CAT, gst.LevelError, "Failed to get request pad from input-selector for new microphone sink")
		return nil
	}
	if err := e.SipCompositorMicrophone.InputSelector.SetProperty("active-pad", sink); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set active-pad property on input-selector for new microphone sink: %v", err))
	}

	gpad := gst.NewGhostPadFromTemplate(name, sink, templ)
	if gpad == nil {
		self.Log(CAT, gst.LevelError, "Failed to create ghost pad for microphone sink")
		return nil
	}
	if !gpad.SetActive(true) {
		self.Log(CAT, gst.LevelError, "Failed to activate ghost pad for microphone sink")
		return nil
	}
	if !self.AddPad(gpad.Pad) {
		self.Log(CAT, gst.LevelError, "Failed to add ghost pad for microphone sink to bin")
		return nil
	}

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Created new microphone sink pad %s", gpad.GetName()))

	return gpad.Pad
}

func (e *SipCompositor) releaseMicrophoneSinkPad(self *gst.Bin, gpad *gst.GhostPad) {
	if e.SipCompositorMicrophone == nil {
		self.Log(CAT, gst.LevelWarning, "Attempted to release microphone sink pad but microphone compositor is not initialized")
		return
	}

	target := gpad.GetTarget()
	if target == nil {
		self.Log(CAT, gst.LevelWarning, "Attempted to release microphone sink pad but it has no target")
		return
	}

	e.SipCompositorMicrophone.InputSelector.ReleaseRequestPad(target)
	if !self.RemovePad(gpad.Pad) {
		self.Log(CAT, gst.LevelWarning, "Failed to remove ghost pad for microphone sink from bin")
		return
	}
	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Released microphone sink pad %s", gpad.GetName()))

	sinks, err := e.SipCompositorMicrophone.InputSelector.GetSinkPads()
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get sink pads from input-selector: %v", err))
		return
	}
	if len(sinks) == 0 {
		self.Log(CAT, gst.LevelInfo, "No more active microphone sink pads, disabling microphone")
		e.cleanupMicrophone(self)
	} else {
		if err := e.SipCompositorMicrophone.InputSelector.SetProperty("active-pad", sinks[len(sinks)-1]); err != nil {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set active-pad property on input-selector after releasing microphone sink pad: %v", err))
		}
	}
}

func (e *SipCompositor) cleanupMicrophone(self *gst.Bin) {
	if e.SipCompositorMicrophone == nil {
		return
	}

	if err := e.SipCompositorMicrophone.InputSelector.SetState(gst.StateNull); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set state of input-selector to NULL during microphone cleanup: %v", err))
	}
	if err := self.Remove(e.SipCompositorMicrophone.InputSelector); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to remove input-selector from bin during microphone cleanup: %v", err))
	}

	gpad := self.GetStaticPad(fmt.Sprintf("src_%d", livekit.TrackSource_MICROPHONE))
	if gpad != nil {
		if !self.RemovePad(gpad) {
			self.Log(CAT, gst.LevelWarning, "Failed to remove ghost pad for microphone source from bin during cleanup")
		}
	}

	e.SipCompositorMicrophone = nil
	self.Log(CAT, gst.LevelInfo, "Cleaned up microphone compositor")
}
