package livekitcompositor

import (
	"fmt"
	"math"
	"sync/atomic"

	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/livekit"
)

type LivekitCompositorScreenshare struct {
	FallbackSwitch *gst.Element
	Filter         *gst.Element
	priority       atomic.Int64
	gpad           *gst.GhostPad
}

func (e *LivekitCompositor) initScreenshare(self *gst.Bin) error {
	if e.LivekitCompositorScreenshare != nil {
		return nil
	}

	self.Log(CAT, gst.LevelInfo, "Initializing screenshare compositor")
	e.LivekitCompositorScreenshare = &LivekitCompositorScreenshare{}

	e.LivekitCompositorScreenshare.priority.Store(math.MaxInt64)

	var err error
	e.LivekitCompositorScreenshare.FallbackSwitch, err = gst.NewElementWithProperties("fallbackswitch", map[string]interface{}{})
	if err != nil {
		return err
	}

	e.LivekitCompositorScreenshare.Filter, err = gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString(fmt.Sprintf("video/x-raw, width=(int)%d, height=(int)%d, framerate=%d/1", e.videoWidth, e.videoHeight, e.videoFramerate)),
	})
	if err != nil {
		return err
	}

	if err := self.AddMany(e.LivekitCompositorScreenshare.FallbackSwitch, e.LivekitCompositorScreenshare.Filter); err != nil {
		return fmt.Errorf("failed to add elements to bin: %w", err)
	}

	if err := e.LivekitCompositorScreenshare.FallbackSwitch.Link(e.LivekitCompositorScreenshare.Filter); err != nil {
		return fmt.Errorf("failed to link fallbackswitch and capsfilter: %w", err)
	}

	class := gst.ToElementClass(self.Class())
	gpad := gst.NewGhostPadFromTemplate(fmt.Sprintf("src_%d", livekit.TrackSource_SCREEN_SHARE), e.LivekitCompositorScreenshare.Filter.GetStaticPad("src"), class.GetPadTemplate("src_%u"))
	if gpad == nil {
		return fmt.Errorf("failed to create ghost pad for screenshare source")
	}
	e.LivekitCompositorScreenshare.gpad = gpad
	if !gpad.SetActive(true) {
		return fmt.Errorf("failed to activate ghost pad for screenshare source")
	}
	if !self.AddPad(gpad.Pad) {
		return fmt.Errorf("failed to add ghost pad for screenshare source to bin")
	}

	if !e.LivekitCompositorScreenshare.FallbackSwitch.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, "Failed to sync state of fallbackswitch with parent")
	}
	if !e.LivekitCompositorScreenshare.Filter.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, "Failed to sync state of capsfilter with parent")
	}

	return nil
}

func (e *LivekitCompositor) requestNewScreenshareSinkPad(self *gst.Bin, templ *gst.PadTemplate, name string) *gst.Pad {
	if err := e.initScreenshare(self); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to initialize screenshare compositor: %v", err))
		return nil
	}

	sink := e.LivekitCompositorScreenshare.FallbackSwitch.GetRequestPad("sink_%u")
	if sink == nil {
		self.Log(CAT, gst.LevelError, "Failed to request new sink pad from fallbackswitch")
		return nil
	}
	if err := sink.SetProperty("priority", uint(e.LivekitCompositorScreenshare.priority.Add(-1))); err != nil {
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

func (e *LivekitCompositor) releaseScreenshareSinkPad(self *gst.Bin, gpad *gst.GhostPad) {
	if e.LivekitCompositorScreenshare == nil {
		self.Log(CAT, gst.LevelWarning, "Attempted to release screenshare sink pad but screenshare compositor is not initialized")
		return
	}

	target := gpad.GetTarget()
	if target == nil {
		self.Log(CAT, gst.LevelWarning, "Attempted to release screenshare sink pad but it has no target")
		return
	}

	e.LivekitCompositorScreenshare.FallbackSwitch.ReleaseRequestPad(target)
	if !self.RemovePad(gpad.Pad) {
		self.Log(CAT, gst.LevelWarning, "Failed to remove ghost pad for screenshare sink from bin")
		return
	}
	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Released screenshare sink pad %s", gpad.GetName()))

	e.cleanupScreenshare(self)
}

func (e *LivekitCompositor) applyScreenshareLayout(self *gst.Bin, layout []string) {}

func (e *LivekitCompositor) cleanupScreenshare(self *gst.Bin) {
	if e.LivekitCompositorScreenshare == nil {
		return
	}

	sinks, err := e.LivekitCompositorScreenshare.FallbackSwitch.GetSinkPads()
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get sink pads from fallbackswitch: %v", err))
		return
	}
	if len(sinks) > 0 {
		return
	}

	if err := e.LivekitCompositorScreenshare.FallbackSwitch.SetState(gst.StateNull); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set fallbackswitch to null state after releasing last screenshare sink pad: %v", err))
	}
	if err := e.LivekitCompositorScreenshare.Filter.SetState(gst.StateNull); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set capsfilter to null state after releasing last screenshare sink pad: %v", err))
	}
	if err := self.RemoveMany(e.LivekitCompositorScreenshare.FallbackSwitch, e.LivekitCompositorScreenshare.Filter); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to remove fallbackswitch from bin after releasing last screenshare sink pad: %v", err))
	}
	if !self.RemovePad(e.LivekitCompositorScreenshare.gpad.Pad) {
		self.Log(CAT, gst.LevelWarning, "Failed to remove ghost pad for screenshare source from bin after releasing last screenshare sink pad")
	}

	e.LivekitCompositorScreenshare = nil
}
