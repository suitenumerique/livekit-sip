package sipcompositor

import (
	"fmt"
	"math"
	"sync/atomic"

	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/livekit"
)

type SipCompositorMicrophone struct {
	FallbackSwitch *gst.Element
	priority       atomic.Int64
}

func (e *SipCompositor) initMicrophone(self *gst.Bin) error {
	if e.SipCompositorMicrophone != nil {
		return nil
	}

	self.Log(CAT, gst.LevelInfo, "Initializing microphone compositor")
	e.SipCompositorMicrophone = &SipCompositorMicrophone{}
	e.SipCompositorMicrophone.priority.Store(math.MaxUint32)

	var err error
	e.SipCompositorMicrophone.FallbackSwitch, err = gst.NewElementWithProperties("fallbackswitch", map[string]interface{}{})
	if err != nil {
		return err
	}

	if err := self.Add(e.SipCompositorMicrophone.FallbackSwitch); err != nil {
		return fmt.Errorf("failed to add fallbackswitch to bin: %w", err)
	}

	class := gst.ToElementClass(self.Class())
	gpad := gst.NewGhostPadFromTemplate(fmt.Sprintf("src_%d", livekit.TrackSource_MICROPHONE), e.SipCompositorMicrophone.FallbackSwitch.GetStaticPad("src"), class.GetPadTemplate("src_%u"))
	if gpad == nil {
		return fmt.Errorf("failed to create ghost pad for microphone source")
	}
	if !gpad.SetActive(true) {
		return fmt.Errorf("failed to activate ghost pad for microphone source")
	}
	if !self.AddPad(gpad.Pad) {
		return fmt.Errorf("failed to add ghost pad for microphone source to bin")
	}

	if !e.SipCompositorMicrophone.FallbackSwitch.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, "Failed to sync state of fallbackswitch with parent")
	}

	return nil
}

func (e *SipCompositor) requestNewMicrophoneSinkPad(self *gst.Bin, templ *gst.PadTemplate, name string) *gst.Pad {
	if err := e.initMicrophone(self); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to initialize microphone compositor: %v", err))
		return nil
	}

	sink := e.SipCompositorMicrophone.FallbackSwitch.GetRequestPad("sink_%u")
	if sink == nil {
		self.Log(CAT, gst.LevelError, "Failed to request new sink pad from fallbackswitch for microphone")
		return nil
	}
	if err := sink.SetProperty("priority", uint(e.SipCompositorMicrophone.priority.Add(-1))); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set priority for microphone sink pad %s: %v", name, err))
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

	e.SipCompositorMicrophone.FallbackSwitch.ReleaseRequestPad(target)
	if !self.RemovePad(gpad.Pad) {
		self.Log(CAT, gst.LevelWarning, "Failed to remove ghost pad for microphone sink from bin")
		return
	}
	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Released microphone sink pad %s", gpad.GetName()))

	sinks, err := e.SipCompositorMicrophone.FallbackSwitch.GetSinkPads()
	if err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to get sink pads from fallbackswitch: %v", err))
		return
	}
	if len(sinks) <= 0 {
		self.Log(CAT, gst.LevelInfo, "No more sink pads on fallbackswitch, cleaning up microphone compositor")
		e.cleanupMicrophone(self)
	}
}

func (e *SipCompositor) cleanupMicrophone(self *gst.Bin) {
	if e.SipCompositorMicrophone == nil {
		return
	}

	if err := e.SipCompositorMicrophone.FallbackSwitch.SetState(gst.StateNull); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set state of fallbackswitch to NULL during microphone cleanup: %v", err))
	}
	if err := self.Remove(e.SipCompositorMicrophone.FallbackSwitch); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to remove fallbackswitch from bin during microphone cleanup: %v", err))
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
