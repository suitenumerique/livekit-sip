package livekitcompositor

import (
	"fmt"
	"time"

	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/audiobus"
)

type LivekitCompositorMicrophone struct {
	SilenceSrc    *gst.Element
	SilenceFilter *gst.Element
	SilencePad    *gst.Pad
	AudioMixer    *gst.Element
	MixFilter     *gst.Element
	Limiter       *gst.Element
	Convert       *gst.Element
	Filter        *gst.Element
	elements      []*gst.Element
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
		"wave":             int(4),                   // silence
		"samplesperbuffer": int(audiobus.Rate / 100), // 10 ms
	})
	if err != nil {
		return fmt.Errorf("failed to create microphone silence source: %w", err)
	}

	compositorMicrophone.SilenceFilter, err = gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString(audiobus.Caps),
	})
	if err != nil {
		return fmt.Errorf("failed to create microphone silence filter: %w", err)
	}

	compositorMicrophone.AudioMixer, err = gst.NewElementWithProperties("audiomixer", map[string]interface{}{
		"force-live":           true,
		"ignore-inactive-pads": true,
		"start-time-selection": int(3), // now
		"latency":              uint64(20 * time.Millisecond),
		"min-upstream-latency": uint64(time.Duration(e.audioJitter) * time.Millisecond),
	})
	if err != nil {
		return err
	}

	compositorMicrophone.MixFilter, err = gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString(audiobus.MixCaps),
	})
	if err != nil {
		return fmt.Errorf("failed to create microphone mix filter: %w", err)
	}

	compositorMicrophone.Limiter, err = gst.NewElement("rglimiter")
	if err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("rglimiter unavailable, mixing without limiter\nerr=%v", err))
		compositorMicrophone.Limiter = nil
	}

	compositorMicrophone.Convert, err = gst.NewElement("audioconvert")
	if err != nil {
		return fmt.Errorf("failed to create microphone audioconvert: %w", err)
	}

	compositorMicrophone.Filter, err = gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString(audiobus.Caps),
	})
	if err != nil {
		return fmt.Errorf("failed to create microphone filter: %w", err)
	}

	chain := []*gst.Element{compositorMicrophone.AudioMixer, compositorMicrophone.MixFilter}
	if compositorMicrophone.Limiter != nil {
		chain = append(chain, compositorMicrophone.Limiter)
	}
	chain = append(chain, compositorMicrophone.Convert, compositorMicrophone.Filter)
	compositorMicrophone.elements = append([]*gst.Element{compositorMicrophone.SilenceSrc, compositorMicrophone.SilenceFilter}, chain...)

	if err := self.AddMany(compositorMicrophone.elements...); err != nil {
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

	if err := gst.ElementLinkMany(chain...); err != nil {
		return fmt.Errorf("failed to link microphone mixer chain: %w", err)
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

	for i := len(compositorMicrophone.elements) - 1; i >= 0; i-- {
		if !compositorMicrophone.elements[i].SyncStateWithParent() {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to sync microphone element state with parent\nname=%s", compositorMicrophone.elements[i].GetName()))
		}
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
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to get sink pads while handling pad-removed signal\nerr=%v", err))
		return
	}
	if len(sinks) > 1 {
		return
	}

	self.Log(CAT, gst.LevelDebug, "Cleaning up microphone compositor since there are no more active sink pads")

	for _, element := range e.LivekitCompositorMicrophone.elements {
		if err := element.SetState(gst.StateNull); err != nil {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set microphone element state to null during cleanup\nname=%s\nerr=%v", element.GetName(), err))
		}
	}
	if err := self.RemoveMany(e.LivekitCompositorMicrophone.elements...); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to remove microphone elements from bin during cleanup\nerr=%v", err))
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
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to initialize microphone compositor\nerr=%v", err))
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

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Created new microphone sink pad\npad=%s", gpad.GetName()))

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
	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Released microphone sink pad\npad=%s", gpad.GetName()))

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
