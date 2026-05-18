package livekitcompositor

import (
	"fmt"

	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/livekit"
)

type LivekitCompositorMicrophone struct {
	SilenceSrc    *gst.Element
	SilenceFilter *gst.Element
	SilencePad    *gst.Pad
	AudioMixer    *gst.Element
	Filter        *gst.Element
}

func (e *LivekitCompositor) initMicrophone(self *gst.Bin) error {
	if e.LivekitCompositorMicrophone != nil {
		return nil
	}

	self.Log(CAT, gst.LevelInfo, "Initializing microphone compositor")
	compositorMicrophone := &LivekitCompositorMicrophone{}

	var err error

	compositorMicrophone.SilenceSrc, err = gst.NewElementWithProperties("audiotestsrc", map[string]interface{}{
		"is-live":          true,
		"wave":             int(4),    // silence
		"samplesperbuffer": uint(160), // 160 bytes = 10 ms at 16 kHz S16LE mono, match audiomixer default output-buffer-duration.
	})
	if err != nil {
		return fmt.Errorf("failed to create microphone silence source: %w", err)
	}

	compositorMicrophone.SilenceFilter, err = gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString("audio/x-raw,format=S16LE,rate=16000,channels=1,layout=interleaved"),
	})
	if err != nil {
		return fmt.Errorf("failed to create microphone silence filter: %w", err)
	}

	compositorMicrophone.AudioMixer, err = gst.NewElementWithProperties("audiomixer", map[string]interface{}{
		"force-live":           true,
		"ignore-inactive-pads": true,
		"start-time-selection": int(3), // now
	})
	if err != nil {
		return err
	}

	compositorMicrophone.Filter, err = gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString("audio/x-raw,format=S16LE,rate=16000,channels=1,layout=interleaved"),
	})
	if err != nil {
		return fmt.Errorf("failed to create microphone filter: %w", err)
	}

	if err := self.AddMany(compositorMicrophone.SilenceSrc, compositorMicrophone.SilenceFilter, compositorMicrophone.AudioMixer, compositorMicrophone.Filter); err != nil {
		return fmt.Errorf("failed to add microphone elements to bin: %w", err)
	}

	if err := compositorMicrophone.SilenceSrc.Link(compositorMicrophone.SilenceFilter); err != nil {
		return fmt.Errorf("failed to link microphone silence source to filter: %w", err)
	}

	compositorMicrophone.SilencePad = compositorMicrophone.AudioMixer.GetRequestPad("sink_0")
	if compositorMicrophone.SilencePad == nil {
		return fmt.Errorf("failed to get request pad from microphone audiomixer")
	}
	if ret := compositorMicrophone.SilenceFilter.GetStaticPad("src").Link(compositorMicrophone.SilencePad); ret != gst.PadLinkOK {
		return fmt.Errorf("failed to link microphone silence filter to audiomixer: %v", ret)
	}

	if err := compositorMicrophone.AudioMixer.Link(compositorMicrophone.Filter); err != nil {
		return fmt.Errorf("failed to link microphone audiomixer to filter: %w", err)
	}

	class := gst.ToElementClass(self.Class())
	gpad := gst.NewGhostPadFromTemplate(fmt.Sprintf("src_%d", livekit.TrackSource_MICROPHONE), compositorMicrophone.Filter.GetStaticPad("src"), class.GetPadTemplate("src_%u"))
	if gpad == nil {
		return fmt.Errorf("failed to create ghost pad for microphone source")
	}
	if !gpad.SetActive(true) {
		return fmt.Errorf("failed to activate ghost pad for microphone source")
	}
	if !self.AddPad(gpad.Pad) {
		return fmt.Errorf("failed to add ghost pad for microphone source to bin")
	}

	if !compositorMicrophone.Filter.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, "Failed to sync microphone filter state with parent")
	}
	if !compositorMicrophone.AudioMixer.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, "Failed to sync microphone audiomixer state with parent")
	}
	if !compositorMicrophone.SilenceFilter.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, "Failed to sync microphone silence filter state with parent")
	}
	if !compositorMicrophone.SilenceSrc.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, "Failed to sync microphone silence source state with parent")
	}

	e.LivekitCompositorMicrophone = compositorMicrophone
	self.Log(CAT, gst.LevelInfo, "Microphone compositor initialized successfully")

	return nil
}

func (e *LivekitCompositor) cleanupMicrophone(self *gst.Bin) {
	if e.LivekitCompositorMicrophone == nil {
		return
	}

	if self.GetCurrentState() == gst.StatePlaying {
		return
	}

	sinks, err := e.LivekitCompositorMicrophone.AudioMixer.GetSinkPads()
	if err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to get sink pads while handling pad-removed signal: %v", err))
		return
	}
	if len(sinks) > 1 {
		return
	}

	self.Log(CAT, gst.LevelDebug, "Cleaning up microphone compositor since there are no more active sink pads")

	if err := e.LivekitCompositorMicrophone.SilenceSrc.SetState(gst.StateNull); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set microphone silence source state to null during cleanup: %v", err))
	}
	if err := e.LivekitCompositorMicrophone.SilenceFilter.SetState(gst.StateNull); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set microphone silence filter state to null during cleanup: %v", err))
	}
	if err := e.LivekitCompositorMicrophone.AudioMixer.SetState(gst.StateNull); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set microphone audiomixer state to null during cleanup: %v", err))
	}
	if err := e.LivekitCompositorMicrophone.Filter.SetState(gst.StateNull); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set microphone filter state to null during cleanup: %v", err))
	}
	if err := self.RemoveMany(e.LivekitCompositorMicrophone.SilenceSrc, e.LivekitCompositorMicrophone.SilenceFilter, e.LivekitCompositorMicrophone.AudioMixer, e.LivekitCompositorMicrophone.Filter); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to remove microphone elements from bin during cleanup: %v", err))
	}

	if pad := self.GetStaticPad(fmt.Sprintf("src_%d", livekit.TrackSource_MICROPHONE)); pad != nil {
		if !self.RemovePad(pad) {
			self.Log(CAT, gst.LevelWarning, "Failed to remove ghost pad for microphone source from bin during cleanup")
		}
	}

	e.LivekitCompositorMicrophone = nil
	self.Log(CAT, gst.LevelInfo, "Cleaned up microphone compositor")
}

func (e *LivekitCompositor) requestNewMicrophoneSinkPad(self *gst.Bin, templ *gst.PadTemplate, name string) *gst.Pad {
	if err := e.initMicrophone(self); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to initialize microphone compositor: %v", err))
		return nil
	}

	sink := e.LivekitCompositorMicrophone.AudioMixer.GetRequestPad("sink_%u")
	if sink == nil {
		self.Log(CAT, gst.LevelError, "Failed to request new sink pad from audiomixer")
		return nil
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

func (e *LivekitCompositor) requestNewRawSinkPad(self *gst.Bin, templ *gst.PadTemplate, name string) *gst.Pad {
	return e.requestNewMicrophoneSinkPad(self, templ, name) // may need to differentiate in the future
}

func (e *LivekitCompositor) requestNewScreenShareAudioSinkPad(self *gst.Bin, templ *gst.PadTemplate, name string) *gst.Pad {
	return e.requestNewMicrophoneSinkPad(self, templ, name) // may need to differentiate in the future
}

func (e *LivekitCompositor) releaseMicrophoneSinkPad(self *gst.Bin, gpad *gst.GhostPad) {
	if e.LivekitCompositorMicrophone == nil {
		self.Log(CAT, gst.LevelWarning, "Attempted to release microphone sink pad but microphone compositor is not initialized")
		return
	}

	target := gpad.GetTarget()
	if target == nil {
		self.Log(CAT, gst.LevelWarning, "Attempted to release microphone sink pad but it has no target")
		return
	}

	e.LivekitCompositorMicrophone.AudioMixer.ReleaseRequestPad(target)
	if !self.RemovePad(gpad.Pad) {
		self.Log(CAT, gst.LevelWarning, "Failed to remove ghost pad for microphone sink from bin")
		return
	}
	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Released microphone sink pad %s", gpad.GetName()))

	e.cleanupMicrophone(self)
}

func (e *LivekitCompositor) releaseRawSinkPad(self *gst.Bin, gpad *gst.GhostPad) {
	e.releaseMicrophoneSinkPad(self, gpad) // may need to differentiate in the future
}

func (e *LivekitCompositor) releaseScreenShareAudioSinkPad(self *gst.Bin, gpad *gst.GhostPad) {
	e.releaseMicrophoneSinkPad(self, gpad) // may need to differentiate in the future
}

func (e *LivekitCompositor) applyMicrophoneLayout(self *gst.Bin, layout []string) {
	return
}
