package sipcompositor

import (
	"fmt"
	"math"
	"sync/atomic"

	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/livekit"
)

type SipCompositorScreenshare struct {
	FallbackSwitch *gst.Element
	Filter         *gst.Element
	priority       atomic.Int64
	gpad           *gst.GhostPad
}

func (e *SipCompositor) initScreenshare(self *gst.Bin) error {
	if e.SipCompositorScreenshare != nil {
		return nil
	}

	self.Log(CAT, gst.LevelInfo, "Initializing screenshare compositor")
	e.SipCompositorScreenshare = &SipCompositorScreenshare{}

	e.SipCompositorScreenshare.priority.Store(math.MaxInt64)

	var err error
	e.SipCompositorScreenshare.FallbackSwitch, err = gst.NewElementWithProperties("fallbackswitch", map[string]interface{}{})
	if err != nil {
		return err
	}

	e.SipCompositorScreenshare.Filter, err = gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString(fmt.Sprintf("video/x-raw, width=(int)%d, height=(int)%d, framerate=%d/1", e.videoWidth, e.videoHeight, e.videoFramerate)),
	})
	if err != nil {
		return err
	}

	if err := self.AddMany(e.SipCompositorScreenshare.FallbackSwitch, e.SipCompositorScreenshare.Filter); err != nil {
		return fmt.Errorf("failed to add elements to bin: %w", err)
	}

	if err := e.SipCompositorScreenshare.FallbackSwitch.Link(e.SipCompositorScreenshare.Filter); err != nil {
		return fmt.Errorf("failed to link fallbackswitch and capsfilter: %w", err)
	}

	class := gst.ToElementClass(self.Class())
	gpad := gst.NewGhostPadFromTemplate(fmt.Sprintf("src_%d", livekit.TrackSource_SCREEN_SHARE), e.SipCompositorScreenshare.Filter.GetStaticPad("src"), class.GetPadTemplate("src_%u"))
	if gpad == nil {
		return fmt.Errorf("failed to create ghost pad for screenshare source")
	}
	e.SipCompositorScreenshare.gpad = gpad
	if !gpad.SetActive(true) {
		return fmt.Errorf("failed to activate ghost pad for screenshare source")
	}
	if !self.AddPad(gpad.Pad) {
		return fmt.Errorf("failed to add ghost pad for screenshare source to bin")
	}

	if !e.SipCompositorScreenshare.FallbackSwitch.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, "Failed to sync state of fallbackswitch with parent")
	}
	if !e.SipCompositorScreenshare.Filter.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, "Failed to sync state of capsfilter with parent")
	}

	return nil
}

func (e *SipCompositor) requestNewScreenshareSinkPad(self *gst.Bin, templ *gst.PadTemplate, name string) *gst.Pad {
	if err := e.initScreenshare(self); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to initialize screenshare compositor: %v", err))
		return nil
	}

	sink := e.SipCompositorScreenshare.FallbackSwitch.GetRequestPad("sink_%u")
	if sink == nil {
		self.Log(CAT, gst.LevelError, "Failed to request new sink pad from fallbackswitch")
		return nil
	}
	if err := sink.SetProperty("priority", uint(e.SipCompositorScreenshare.priority.Add(-1))); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set priority property on new screenshare sink pad: %v", err))
	}

	gpad := gst.NewGhostPadFromTemplate(name, sink, templ)
	if gpad == nil {
		self.Log(CAT, gst.LevelError, "Failed to create ghost pad for screenshare sink")
		return nil
	}
	if !gpad.SetActive(true) {
		self.Log(CAT, gst.LevelError, "Failed to activate ghost pad for screenshare sink")
		return nil
	}
	if !self.AddPad(gpad.Pad) {
		self.Log(CAT, gst.LevelError, "Failed to add ghost pad for screenshare sink to bin")
		return nil
	}

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Created new screenshare sink pad %s", gpad.GetName()))

	return gpad.Pad
}

func (e *SipCompositor) releaseScreenshareSinkPad(self *gst.Bin, gpad *gst.GhostPad) {
	if e.SipCompositorScreenshare == nil {
		self.Log(CAT, gst.LevelWarning, "Attempted to release screenshare sink pad but screenshare compositor is not initialized")
		return
	}

	target := gpad.GetTarget()
	if target == nil {
		self.Log(CAT, gst.LevelWarning, "Attempted to release screenshare sink pad but it has no target")
		return
	}

	e.SipCompositorScreenshare.FallbackSwitch.ReleaseRequestPad(target)
	if !self.RemovePad(gpad.Pad) {
		self.Log(CAT, gst.LevelWarning, "Failed to remove ghost pad for screenshare sink from bin")
		return
	}
	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Released screenshare sink pad %s", gpad.GetName()))

	sinks, err := e.SipCompositorScreenshare.FallbackSwitch.GetSinkPads()
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get sink pads from fallbackswitch: %v", err))
		return
	}
	if len(sinks) == 0 {
		self.Log(CAT, gst.LevelInfo, "No more active screenshare sink pads, disabling screenshare")
		e.cleanupScreenshare(self)
	}
}

func (e *SipCompositor) cleanupScreenshare(self *gst.Bin) {
	if e.SipCompositorScreenshare == nil {
		return
	}

	if err := e.SipCompositorScreenshare.FallbackSwitch.SetState(gst.StateNull); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set fallbackswitch to null state after releasing last screenshare sink pad: %v", err))
	}
	if err := e.SipCompositorScreenshare.Filter.SetState(gst.StateNull); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set capsfilter to null state after releasing last screenshare sink pad: %v", err))
	}
	if err := self.RemoveMany(e.SipCompositorScreenshare.FallbackSwitch, e.SipCompositorScreenshare.Filter); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to remove fallbackswitch from bin after releasing last screenshare sink pad: %v", err))
	}
	if !self.RemovePad(e.SipCompositorScreenshare.gpad.Pad) {
		self.Log(CAT, gst.LevelWarning, "Failed to remove ghost pad for screenshare source from bin after releasing last screenshare sink pad")
	}

	e.SipCompositorScreenshare = nil
}
