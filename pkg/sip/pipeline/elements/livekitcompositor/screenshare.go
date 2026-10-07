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
	"github.com/livekit/sip/pkg/i18n"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/keyframe"
	"github.com/livekit/sip/pkg/sip/pipeline/metrics"
	"github.com/samber/lo"
	"github.com/vopenia-io/go-pangocairo/cairo"
	"github.com/vopenia-io/go-pangocairo/pango"
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
	// screenshareMessageDelay is how long the last presenter frame stays on
	// screen before the end of screenshare message replaces it.
	screenshareMessageDelay = 500 * time.Millisecond
	// screenshareMessageFramerate is the frame rate of the end of screenshare message source.
	screenshareMessageFramerate = 5
	// screenshareMessageRefWidth is the frame width the message text is rendered for.
	screenshareMessageRefWidth = 1920
	// screenshareBlankMaxSize is the largest width and height of the black
	// keyframes the SFU sends when a published track stops.
	screenshareBlankMaxSize = 16
	// screenshareMessagePriority is the fallbackswitch priority of the message
	// pad, below every presenter pad.
	screenshareMessagePriority = uint(math.MaxUint32)
)

// screenshareState carries the counters that deferred timers observe. It holds
// no GStreamer wrapper so a pending timer never keeps the pipeline alive.
type screenshareState struct {
	frames     atomic.Int64  // presenter buffers that left the fallbackswitch
	generation atomic.Uint64 // bumped on every sink pad request/release
	width      atomic.Int32  // size of the last presenter frame
	height     atomic.Int32
	message    atomic.Bool // end of screenshare message attached
}

type LivekitCompositorScreenshare struct {
	FallbackSwitch *gst.Element
	Freeze         *gst.Element
	Filter         *gst.Element
	priority       atomic.Int64
	gpad           *gst.GhostPad
	state          *screenshareState
	message        *screenshareMessage
}

// screenshareMessage is the live source that shows the end of screenshare
// message on a fallbackswitch pad of the lowest priority.
type screenshareMessage struct {
	Src       *gst.Element
	SrcFilter *gst.Element
	Overlay   *gst.Element
	Convert   *gst.Element
	OutFilter *gst.Element
	pad       *gst.Pad
}

func (m *screenshareMessage) elements() []*gst.Element {
	return []*gst.Element{m.Src, m.SrcFilter, m.Overlay, m.Convert, m.OutFilter}
}

func (e *LivekitCompositor) initScreenshare(self *gst.Bin) error {
	if e.LivekitCompositorScreenshare != nil {
		return nil
	}

	self.Log(CAT, gst.LevelInfo, "Initializing screenshare compositor")
	e.LivekitCompositorScreenshare = &LivekitCompositorScreenshare{state: &screenshareState{}}

	e.LivekitCompositorScreenshare.priority.Store(math.MaxInt64)

	var err error
	e.LivekitCompositorScreenshare.FallbackSwitch, err = gst.NewElementWithProperties("fallbackswitch", map[string]interface{}{
		"immediate-fallback": true,
	})
	if err != nil {
		return err
	}

	// On active pad change, request a keyframe from the newly active source,
	// and remove the end of screenshare message once a presenter is active.
	st := e.LivekitCompositorScreenshare.state
	wself := glib.WeakRefInit(self)
	we := weak.Make(e)
	if _, err := e.LivekitCompositorScreenshare.FallbackSwitch.Connect("notify::active-pad", func(elem *gst.Element, _ *glib.ParamSpec) {
		pad := elem.GetStaticPad("src")
		if pad == nil {
			return
		}
		keyframe.ForceKeyUnit(pad)
		if !st.message.Load() || !presenterActive(elem) {
			return
		}
		self := gst.ToGstBin(wself.Get())
		e := we.Value()
		if self == nil || self.Instance() == nil || e == nil {
			return
		}
		e.scheduleHideScreenshareMessage(self)
	}); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to connect to notify::active-pad signal of fallbackswitch\nerr=%v", err))
	}

	// Live frame repeater: outputs the latest presenter frame at the output
	// framerate, whatever the presenter frame rate.
	e.LivekitCompositorScreenshare.Freeze, err = gst.NewElementWithProperties("imagefreeze", map[string]interface{}{
		"is-live":       true,
		"allow-replace": true,
	})
	if err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("imagefreeze unavailable, screenshare output follows the presenter frame rate\nerr=%v", err))
		e.LivekitCompositorScreenshare.Freeze = nil
	}

	e.LivekitCompositorScreenshare.Filter, err = gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString(fmt.Sprintf("video/x-raw, width=(int)[1,%d], height=(int)[1,%d], framerate=%d/1", e.screenshareWidth, e.screenshareHeight, e.screenshareFramerate)),
	})
	if err != nil {
		return err
	}

	chain := e.LivekitCompositorScreenshare.elements()
	if err := self.AddMany(chain...); err != nil {
		return fmt.Errorf("failed to add elements to bin: %w", err)
	}

	if err := gst.ElementLinkMany(chain...); err != nil {
		return fmt.Errorf("failed to link screenshare chain: %w", err)
	}

	class := gst.ToElementClass(self.Class())
	gpad := gst.NewGhostPadFromTemplate(fmt.Sprintf("src_%d", livekit.TrackSource_SCREEN_SHARE), e.LivekitCompositorScreenshare.Filter.GetStaticPad("src"), class.GetPadTemplate("src_%u"))
	if gpad == nil {
		return fmt.Errorf("failed to create ghost pad for screenshare source")
	}
	e.LivekitCompositorScreenshare.gpad = gpad
	e.LivekitCompositorScreenshare.FallbackSwitch.GetStaticPad("src").AddProbe(gst.PadProbeTypeBuffer|gst.PadProbeTypeBufferList, func(_ *gst.Pad, _ *gst.PadProbeInfo) gst.PadProbeReturn {
		if !st.message.Load() {
			st.frames.Add(1)
		}
		return gst.PadProbeOK
	})
	if !gpad.SetActive(true) {
		return fmt.Errorf("failed to activate ghost pad for screenshare source")
	}
	if !self.AddPad(gpad.Pad) {
		return fmt.Errorf("failed to add ghost pad for screenshare source to bin")
	}

	for _, element := range chain {
		if !element.SyncStateWithParent() {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to sync state of screenshare element with parent\nname=%s", element.GetName()))
		}
	}

	return nil
}

// elements lists the screenshare chain from the fallbackswitch to the output capsfilter.
func (ss *LivekitCompositorScreenshare) elements() []*gst.Element {
	chain := []*gst.Element{ss.FallbackSwitch}
	if ss.Freeze != nil {
		chain = append(chain, ss.Freeze)
	}
	return append(chain, ss.Filter)
}

func (e *LivekitCompositor) requestNewScreenshareSinkPad(self *gst.Bin, templ *gst.PadTemplate, name string) *gst.Pad {
	if err := e.initScreenshare(self); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to initialize screenshare compositor\nerr=%v", err))
		return nil
	}

	ss := e.LivekitCompositorScreenshare
	presenters, err := ss.presenterPads()
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
	dropBlankFrames(sink, ss.state)

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

// scheduleScreenshareCleanup shows the end of screenshare message once no
// presenter pad has been requested for screenshareMessageDelay, and tears the
// screenshare chain down after screenshareGrace. Runs on the GLib main loop
// like every other compositor pad change.
func (e *LivekitCompositor) scheduleScreenshareCleanup(self *gst.Bin) {
	ss := e.LivekitCompositorScreenshare
	if ss == nil {
		return
	}
	sinks, err := ss.presenterPads()
	if err != nil || len(sinks) > 0 {
		return
	}
	st := ss.state
	gen := st.generation.Add(1)
	wself := glib.WeakRefInit(self)
	we := weak.Make(e)
	time.AfterFunc(screenshareMessageDelay, func() {
		if _, err := glib.IdleAdd(func() {
			self := gst.ToGstBin(wself.Get())
			e := we.Value()
			if self == nil || self.Instance() == nil || e == nil {
				return
			}
			e.mu.Lock()
			defer e.mu.Unlock()
			if cur := e.LivekitCompositorScreenshare; cur == nil || cur.state != st || st.generation.Load() != gen {
				return
			}
			e.showScreenshareMessage(self)
		}); err != nil {
			CAT.Log(gst.LevelError, fmt.Sprintf("Failed to schedule end of screenshare message\nerr=%v", err))
		}
	})
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
	e.hideScreenshareMessage(self)

	sinks, err := e.LivekitCompositorScreenshare.presenterPads()
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get sink pads from fallbackswitch\nerr=%v", err))
		return
	}
	if len(sinks) > 0 {
		return
	}
	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Tearing down screenshare compositor\nframes=%d", e.LivekitCompositorScreenshare.state.frames.Load()))

	chain := e.LivekitCompositorScreenshare.elements()
	for _, element := range chain {
		if err := element.SetState(gst.StateNull); err != nil {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set screenshare element to null state after releasing last screenshare sink pad\nname=%s\nerr=%v", element.GetName(), err))
		}
	}
	if err := self.RemoveMany(chain...); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to remove screenshare chain from bin after releasing last screenshare sink pad\nerr=%v", err))
	}
	if !self.RemovePad(e.LivekitCompositorScreenshare.gpad.Pad) {
		self.Log(CAT, gst.LevelWarning, "Failed to remove ghost pad for screenshare source from bin after releasing last screenshare sink pad")
	}

	e.LivekitCompositorScreenshare = nil
}

// presenterPads lists the fallbackswitch sink pads fed by a presenter, leaving
// out the end of screenshare message pad.
func (ss *LivekitCompositorScreenshare) presenterPads() ([]*gst.Pad, error) {
	sinks, err := ss.FallbackSwitch.GetSinkPads()
	if err != nil {
		return nil, err
	}
	return lo.Filter(sinks, func(pad *gst.Pad, _ int) bool {
		v, err := pad.GetProperty("priority")
		priority, ok := v.(uint)
		return err != nil || !ok || priority != screenshareMessagePriority
	}), nil
}

// activePad returns the active pad of the fallbackswitch, if any.
func activePad(fallbackSwitch *gst.Element) *gst.Pad {
	v, err := fallbackSwitch.GetProperty("active-pad")
	if err != nil {
		return nil
	}
	pad := gst.ToPad(v)
	if pad == nil || pad.Instance() == nil {
		return nil
	}
	return pad
}

// presenterActive reports whether the active pad of the fallbackswitch is fed
// by a presenter.
func presenterActive(fallbackSwitch *gst.Element) bool {
	pad := activePad(fallbackSwitch)
	if pad == nil {
		return false
	}
	p, err := pad.GetProperty("priority")
	priority, ok := p.(uint)
	return err == nil && ok && priority != screenshareMessagePriority
}

// dropBlankFrames drops the SFU black keyframes, and their caps, on a
// presenter pad, and records the size of the presenter frames.
func dropBlankFrames(pad *gst.Pad, st *screenshareState) {
	blank := false
	pad.AddProbe(gst.PadProbeTypeBuffer|gst.PadProbeTypeBufferList|gst.PadProbeTypeEventDownstream, func(_ *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		if info.Type()&gst.PadProbeTypeEventDownstream == 0 {
			if blank {
				return gst.PadProbeDrop
			}
			return gst.PadProbeOK
		}
		ev := info.GetEvent()
		if ev == nil || ev.Type() != gst.EventTypeCaps {
			return gst.PadProbeOK
		}
		width, height, ok := capsSize(ev.ParseCaps())
		if !ok {
			return gst.PadProbeOK
		}
		blank = width <= screenshareBlankMaxSize && height <= screenshareBlankMaxSize
		if blank {
			return gst.PadProbeDrop
		}
		st.width.Store(int32(width))
		st.height.Store(int32(height))
		return gst.PadProbeOK
	})
}

func capsSize(caps *gst.Caps) (int, int, bool) {
	if caps == nil || caps.GetSize() == 0 {
		return 0, 0, false
	}
	st := caps.GetStructureAt(0)
	w, errW := st.GetValue("width")
	h, errH := st.GetValue("height")
	if errW != nil || errH != nil {
		return 0, 0, false
	}
	width, okW := w.(int)
	height, okH := h.(int)
	return width, height, okW && okH
}

// showScreenshareMessage feeds the end of screenshare message to the
// fallbackswitch at the size of the last presenter frame. Called with e.mu held.
func (e *LivekitCompositor) showScreenshareMessage(self *gst.Bin) {
	ss := e.LivekitCompositorScreenshare
	if ss == nil || ss.message != nil {
		return
	}
	if presenters, err := ss.presenterPads(); err != nil || len(presenters) > 0 {
		return
	}
	width, height := int(ss.state.width.Load()), int(ss.state.height.Load())
	if width == 0 || height == 0 {
		width, height = int(e.screenshareWidth), int(e.screenshareHeight)
	}
	if e.screenshareMessageText == nil {
		e.screenshareMessageText = renderScreenshareMessageText(e.lang)
	}
	text := e.screenshareMessageText

	m := &screenshareMessage{}
	var err error
	if m.Src, err = gst.NewElementWithProperties("videotestsrc", map[string]interface{}{
		"is-live": true,
		"pattern": int(2), // black
	}); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to create end of screenshare message source\nerr=%v", err))
		return
	}
	if m.SrcFilter, err = gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString(fmt.Sprintf("video/x-raw, format=BGRx, width=%d, height=%d, framerate=%d/1", width, height, screenshareMessageFramerate)),
	}); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to create end of screenshare message capsfilter\nerr=%v", err))
		return
	}
	if m.Overlay, err = gst.NewElement("cairooverlay"); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to create end of screenshare message overlay\nerr=%v", err))
		return
	}
	if m.Convert, err = gst.NewElement("videoconvert"); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to create end of screenshare message videoconvert\nerr=%v", err))
		return
	}
	if m.OutFilter, err = gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString("video/x-raw, format=I420"),
	}); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to create end of screenshare message output capsfilter\nerr=%v", err))
		return
	}
	m.Overlay.Connect("draw", func(_ *gst.Element, cr *cairo.Context, _ gst.ClockTime) {
		drawScreenshareMessage(cr, width, height, text)
	})

	chain := m.elements()
	if err := self.AddMany(chain...); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to add end of screenshare message to bin\nerr=%v", err))
		return
	}
	if err := gst.ElementLinkMany(chain...); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to link end of screenshare message chain\nerr=%v", err))
		_ = self.RemoveMany(chain...)
		return
	}
	m.pad = ss.FallbackSwitch.GetRequestPad("sink_%u")
	if m.pad == nil {
		self.Log(CAT, gst.LevelWarning, "Failed to request fallbackswitch pad for end of screenshare message")
		_ = self.RemoveMany(chain...)
		return
	}
	if err := m.pad.SetProperty("priority", screenshareMessagePriority); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set priority property on end of screenshare message pad\nerr=%v", err))
	}
	if ret := m.OutFilter.GetStaticPad("src").Link(m.pad); ret != gst.PadLinkOK {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to link end of screenshare message to fallbackswitch\nret=%v", ret))
		ss.FallbackSwitch.ReleaseRequestPad(m.pad)
		_ = self.RemoveMany(chain...)
		return
	}
	ss.message = m
	ss.state.message.Store(true)
	for _, element := range chain {
		if !element.SyncStateWithParent() {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to sync state of end of screenshare message element with parent\nname=%s", element.GetName()))
		}
	}
	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Showing end of screenshare message\nwidth=%d\nheight=%d", width, height))
}

// scheduleHideScreenshareMessage removes the end of screenshare message from
// the GLib main loop.
func (e *LivekitCompositor) scheduleHideScreenshareMessage(self *gst.Bin) {
	wself := glib.WeakRefInit(self)
	we := weak.Make(e)
	if _, err := glib.IdleAdd(func() {
		self := gst.ToGstBin(wself.Get())
		e := we.Value()
		if self == nil || self.Instance() == nil || e == nil {
			return
		}
		e.hideScreenshareMessage(self)
	}); err != nil {
		CAT.Log(gst.LevelError, fmt.Sprintf("Failed to schedule end of screenshare message removal\nerr=%v", err))
	}
}

// hideScreenshareMessage stops and removes the end of screenshare message
// source. Runs on the GLib main loop.
func (e *LivekitCompositor) hideScreenshareMessage(self *gst.Bin) {
	ss := e.LivekitCompositorScreenshare
	if ss == nil || ss.message == nil {
		return
	}
	m := ss.message
	ss.message = nil
	if active := activePad(ss.FallbackSwitch); active == nil || active.GetName() != m.pad.GetName() {
		m.pad.SendEvent(gst.NewFlushStartEvent())
	}
	chain := m.elements()
	for _, element := range chain {
		if err := element.SetState(gst.StateNull); err != nil {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set end of screenshare message element to null state\nname=%s\nerr=%v", element.GetName(), err))
		}
	}
	ss.FallbackSwitch.ReleaseRequestPad(m.pad)
	if err := self.RemoveMany(chain...); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to remove end of screenshare message from bin\nerr=%v", err))
	}
	ss.state.message.Store(false)
	self.Log(CAT, gst.LevelInfo, "Removed end of screenshare message")
}

// renderScreenshareMessageText renders the end of screenshare message in white
// for a screenshareMessageRefWidth wide frame.
func renderScreenshareMessageText(lang string) *cairo.Surface {
	probe := cairo.Create(cairo.CreateImageSurface(cairo.FORMAT_ARGB32, 1, 1))
	layout := pango.CairoCreateLayout(probe)
	desc := pango.FontDescriptionFromString("Sans Bold")
	desc.SetSize(int(float64(screenshareMessageRefWidth) / 1280 * 14 * float64(pango.SCALE)))
	layout.SetFontDescription(desc)
	layout.SetText(i18n.Printer(lang).Sprintf("Screen sharing has ended"), -1)
	pw, ph := layout.GetSize()

	surface := cairo.CreateImageSurface(cairo.FORMAT_ARGB32, pw/pango.SCALE+1, ph/pango.SCALE+1)
	cr := cairo.Create(surface)
	pango.CairoUpdateLayout(cr, layout)
	cr.SetSourceRGBA(1, 1, 1, 1)
	cr.MoveTo(0, 0)
	pango.CairoShowLayout(cr, layout)
	surface.Flush()
	return surface
}

// drawScreenshareMessage paints the message background and the centered text.
func drawScreenshareMessage(cr *cairo.Context, width, height int, text *cairo.Surface) {
	cr.Save()
	cr.SetSourceRGBA(bgColorR, bgColorG, bgColorB, 1.0)
	cr.Rectangle(0, 0, float64(width), float64(height))
	cr.Fill()
	scale := float64(width) / screenshareMessageRefWidth
	tw := float64(text.GetWidth()) * scale
	th := float64(text.GetHeight()) * scale
	cr.Translate(float64(width)/2-tw/2, float64(height)/2-th/2)
	cr.Scale(scale, scale)
	cr.SetSourceSurface(text, 0, 0)
	cr.Paint()
	cr.Restore()
}
