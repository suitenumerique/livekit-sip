package livekitcompositor

import (
	"fmt"
	"math"
	"sync/atomic"
	"time"
	"weak"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/keyframe"
	"github.com/livekit/sip/pkg/sip/pipeline/metrics"
)

const (
	// screenshareGrace is how long the screenshare output chain survives after
	// its last presenter pad is released. Web apps stop the previous presenter
	// before the next one publishes: measured on staging, the next presenter's
	// first frame lands 1.2 to 2.3 s after the release. Tearing the chain down
	// in between releases the BFCP floor and rebuilds the SIP encoder, which
	// the device shows as a black screen until the next keyframe.
	screenshareGrace = 3 * time.Second
	// screenshareSwitchWatchdog bounds how long a new presenter may take to
	// deliver its first frame before a keyframe is forced again.
	screenshareSwitchWatchdog = 2 * time.Second
)

// screenshareState carries the counters that deferred timers observe. It holds
// no GStreamer wrapper so a pending timer never keeps the pipeline alive.
type screenshareState struct {
	frames     atomic.Int64  // buffers that left the screenshare chain
	generation atomic.Uint64 // bumped on every sink pad request/release
}

type LivekitCompositorScreenshare struct {
	FallbackSwitch *gst.Element
	Filter         *gst.Element
	priority       atomic.Int64
	gpad           *gst.GhostPad
	state          *screenshareState
}

func (e *LivekitCompositor) initScreenshare(self *gst.Bin) error {
	if e.LivekitCompositorScreenshare != nil {
		return nil
	}

	self.Log(CAT, gst.LevelInfo, "Initializing screenshare compositor")
	e.LivekitCompositorScreenshare = &LivekitCompositorScreenshare{state: &screenshareState{}}

	e.LivekitCompositorScreenshare.priority.Store(math.MaxInt64)

	var err error
	e.LivekitCompositorScreenshare.FallbackSwitch, err = gst.NewElementWithProperties("fallbackswitch", map[string]interface{}{})
	if err != nil {
		return err
	}

	// On active pad change, request a keyframe from the newly active source.
	if _, err := e.LivekitCompositorScreenshare.FallbackSwitch.Connect("notify::active-pad", func(elem *gst.Element, _ *glib.ParamSpec) {
		pad := elem.GetStaticPad("src")
		if pad == nil {
			return
		}
		keyframe.ForceKeyUnit(pad)
	}); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to connect to notify::active-pad signal of fallbackswitch\nerr=%v", err))
	}

	e.LivekitCompositorScreenshare.Filter, err = gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString(fmt.Sprintf("video/x-raw, width=(int)[1,%d], height=(int)[1,%d], framerate=%d/1", e.screenshareWidth, e.screenshareHeight, e.screenshareFramerate)),
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
	st := e.LivekitCompositorScreenshare.state
	gpad.Pad.AddProbe(gst.PadProbeTypeBuffer|gst.PadProbeTypeBufferList, func(_ *gst.Pad, _ *gst.PadProbeInfo) gst.PadProbeReturn {
		st.frames.Add(1)
		return gst.PadProbeOK
	})
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
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to initialize screenshare compositor\nerr=%v", err))
		return nil
	}

	ss := e.LivekitCompositorScreenshare
	presenters, err := ss.FallbackSwitch.GetSinkPads()
	if err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to list screenshare sink pads\nerr=%v", err))
	}
	// A pending cleanup (last presenter just left) or an existing presenter
	// both mean this pad is a presenter switch, not a new share.
	switching := len(presenters) > 0 || ss.state.frames.Load() > 0
	gen := ss.state.generation.Add(1)

	sink := ss.FallbackSwitch.GetRequestPad("sink_%u")
	if sink == nil {
		self.Log(CAT, gst.LevelError, "Failed to request new sink pad from fallbackswitch")
		return nil
	}
	if err := sink.SetProperty("priority", uint(e.LivekitCompositorScreenshare.priority.Add(-1))); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set priority property on new screenshare sink pad\nerr=%v", err))
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

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Created new screenshare sink pad\npad=%s\nswitch=%t\ngeneration=%d", gpad.GetName(), switching, gen))

	if switching {
		e.watchScreenshareSwitch(self, gpad.GetName(), gen)
	}

	return gpad.Pad
}

// watchScreenshareSwitch checks that frames flow again after a presenter
// switch; it forces one more keyframe if they do not, and reports the outcome.
func (e *LivekitCompositor) watchScreenshareSwitch(self *gst.Bin, padName string, gen uint64) {
	st := e.LivekitCompositorScreenshare.state
	wself := glib.WeakRefInit(self)
	start := st.frames.Load()
	startedAt := time.Now()

	report := func(result string) {
		metrics.ScreenshareTransition(result)
		if self := gst.ToGstBin(wself.Get()); self != nil && self.Instance() != nil {
			self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Screenshare presenter switch %s\ngeneration=%d\nelapsed=%s", result, gen, time.Since(startedAt).Round(time.Millisecond)))
		}
	}
	forceKeyUnit := func() {
		self := gst.ToGstBin(wself.Get())
		if self == nil || self.Instance() == nil {
			return
		}
		if pad := self.GetStaticPad(padName); pad != nil {
			keyframe.ForceKeyUnit(pad)
		}
	}

	time.AfterFunc(screenshareSwitchWatchdog, func() {
		if st.generation.Load() != gen {
			return // superseded by another switch or by the end of the share
		}
		if st.frames.Load() > start {
			report("ok")
			return
		}
		forceKeyUnit()
		mid := st.frames.Load()
		time.AfterFunc(screenshareSwitchWatchdog, func() {
			if st.generation.Load() != gen {
				return
			}
			if st.frames.Load() > mid {
				report("reconciled")
			} else {
				report("lost")
			}
		})
	})
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
	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Released screenshare sink pad\npad=%s", gpad.GetName()))

	e.scheduleScreenshareCleanup(self)
}

// scheduleScreenshareCleanup tears the screenshare chain down once no presenter
// pad has been requested for screenshareGrace. Runs on the GLib main loop like
// every other compositor pad change.
func (e *LivekitCompositor) scheduleScreenshareCleanup(self *gst.Bin) {
	ss := e.LivekitCompositorScreenshare
	if ss == nil {
		return
	}
	sinks, err := ss.FallbackSwitch.GetSinkPads()
	if err != nil || len(sinks) > 0 {
		return
	}
	st := ss.state
	gen := st.generation.Add(1)
	wself := glib.WeakRefInit(self)
	we := weak.Make(e)
	time.AfterFunc(screenshareGrace, func() {
		if _, err := glib.IdleAdd(func() {
			self := gst.ToGstBin(wself.Get())
			e := we.Value()
			if self == nil || self.Instance() == nil || e == nil {
				return
			}
			if cur := e.LivekitCompositorScreenshare; cur == nil || cur.state != st || st.generation.Load() != gen {
				return // a new presenter arrived within the grace period
			}
			e.cleanupScreenshare(self)
		}); err != nil {
			CAT.Log(gst.LevelError, fmt.Sprintf("Failed to schedule screenshare cleanup\nerr=%v", err))
		}
	})
}

func (e *LivekitCompositor) applyScreenshareLayout(self *gst.Bin, layout []string) {}

func (e *LivekitCompositor) cleanupScreenshare(self *gst.Bin) {
	if e.LivekitCompositorScreenshare == nil {
		return
	}

	sinks, err := e.LivekitCompositorScreenshare.FallbackSwitch.GetSinkPads()
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get sink pads from fallbackswitch\nerr=%v", err))
		return
	}
	if len(sinks) > 0 {
		return
	}
	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Tearing down screenshare compositor\nframes=%d", e.LivekitCompositorScreenshare.state.frames.Load()))

	if err := e.LivekitCompositorScreenshare.FallbackSwitch.SetState(gst.StateNull); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set fallbackswitch to null state after releasing last screenshare sink pad\nerr=%v", err))
	}
	if err := e.LivekitCompositorScreenshare.Filter.SetState(gst.StateNull); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set capsfilter to null state after releasing last screenshare sink pad\nerr=%v", err))
	}
	if err := self.RemoveMany(e.LivekitCompositorScreenshare.FallbackSwitch, e.LivekitCompositorScreenshare.Filter); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to remove fallbackswitch from bin after releasing last screenshare sink pad\nerr=%v", err))
	}
	if !self.RemovePad(e.LivekitCompositorScreenshare.gpad.Pad) {
		self.Log(CAT, gst.LevelWarning, "Failed to remove ghost pad for screenshare source from bin after releasing last screenshare sink pad")
	}

	e.LivekitCompositorScreenshare = nil
}
