package trackfallback

import (
	"fmt"
	"sync"
	"time"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/livekit"
)

var CAT = gst.NewDebugCategory(
	"trackfallback",
	gst.DebugColorNone,
	"Track Fallback Element",
)

const NbTracks = int(livekit.TrackSource_SCREEN_SHARE_AUDIO) + 1

type TrackFallback struct {
	mu sync.Mutex

	videoWidth     uint
	videoHeight    uint
	videoFramerate uint

	Tracks [NbTracks]struct {
		initialized bool
		enabled     bool
		fallback    Fallback
		fallbackPad *gst.Pad
		element     *gst.Element
		// clockSync   *gst.Element
	}
}

func (e *TrackFallback) New() glib.GoObjectSubclass {
	return &TrackFallback{}
}

func (e *TrackFallback) ClassInit(klass *glib.ObjectClass) {
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"trackfallback",
		"Audio/Video",
		"Provides fallback tracks to ensure media continuity when live tracks are unavailable",
		"Roomkit <roomkit-visio@numerique.gouv.fr>",
	)

	class.AddPadTemplate(gst.NewPadTemplate(
		"sink_%u",
		gst.PadDirectionSink,
		gst.PadPresenceRequest,
		gst.NewAnyCaps(),
	))

	class.AddPadTemplate(gst.NewPadTemplate(
		"src_%u",
		gst.PadDirectionSource,
		gst.PadPresenceSometimes,
		gst.NewAnyCaps(),
	))

	class.InstallProperties(properties)
}

func (e *TrackFallback) InstanceInit(instance *glib.Object) {
	e.videoWidth = 1280
	e.videoHeight = 720
	e.videoFramerate = 24
}

func (e *TrackFallback) Finalize(instance *glib.Object) {
	for i := range e.Tracks {
		e.Tracks[i].fallback = nil
		e.Tracks[i].fallbackPad = nil
		e.Tracks[i].element = nil
	}
}

func (e *TrackFallback) RequestNewPad(instance *gst.Element, templ *gst.PadTemplate, name string, caps *gst.Caps) *gst.Pad {
	self := gst.ToGstBin(instance)

	if templ == nil || templ.GetName() != "sink_%u" {
		tname := "<nil>"
		if templ != nil {
			tname = templ.GetName()
		}
		self.Log(CAT, gst.LevelDebug, fmt.Sprintf("RequestNewPad called with unsupported template\ntemplate=%s", tname))
		return nil
	}

	if name == "" {
		self.Log(CAT, gst.LevelError, "RequestNewPad `sink_%u` called with empty name")
		return nil
	}

	var session int
	if _, err := fmt.Sscanf(name, "sink_%d", &session); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to parse pad name\nname=%s\nerr=%v", name, err))
		return nil
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	kind := livekit.TrackSource(session)
	switch kind {
	case livekit.TrackSource_MICROPHONE,
		livekit.TrackSource_CAMERA,
		livekit.TrackSource_SCREEN_SHARE,
		livekit.TrackSource_SCREEN_SHARE_AUDIO:
	default:
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Unsupported session kind in pad name\nname=%s\nsession=%d\nkind=%s", name, session, kind.String()))
		return nil
	}

	if err := e.initTrack(self, kind); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to initialize fallback for pad\nkind=%s\npad=%s\nerr=%v", kind.String(), name, err))
		self.Error(fmt.Sprintf("Failed to initialize %s fallback for pad %s", kind.String(), name), err)
		return nil
	}

	sink := e.Tracks[kind].element.GetRequestPad("sink_%u")
	if sink == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get request pad for fallback\nkind=%s\npad=%s", kind.String(), name))
		self.Error(fmt.Sprintf("Failed to get request pad for %s fallback for pad %s", kind.String(), name), fmt.Errorf("request pad returned nil"))
		return nil
	}

	if err := sink.SetProperty("priority", uint32(0)); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to set priority property on fallback pad\nkind=%s\npad=%s\nerr=%v", kind.String(), name, err))
		self.Error(fmt.Sprintf("Failed to set priority property on %s fallback pad for pad %s", kind.String(), name), err)
		return nil
	}

	gpad := gst.NewGhostPadFromTemplate(name, sink, templ)
	if gpad == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create ghost pad for fallback\nkind=%s\npad=%s", kind.String(), name))
		self.Error(fmt.Sprintf("Failed to create ghost pad for %s fallback for pad %s", kind.String(), name), fmt.Errorf("ghost pad returned nil"))
		return nil
	}
	if !gpad.SetActive(true) {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to activate ghost pad for fallback\nkind=%s\npad=%s", kind.String(), name))
		self.Error(fmt.Sprintf("Failed to activate ghost pad for %s fallback for pad %s", kind.String(), name), fmt.Errorf("failed to activate ghost pad"))
		return nil
	}
	if !self.AddPad(gpad.Pad) {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add ghost pad for fallback to bin\nkind=%s\npad=%s", kind.String(), name))
		self.Error(fmt.Sprintf("Failed to add ghost pad for %s fallback to bin for pad %s", kind.String(), name), fmt.Errorf("failed to add ghost pad to bin"))
		return nil
	}

	self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Successfully created and added ghost pad for fallback\npad=%s\nkind=%s", name, kind.String()))

	return gpad.Pad
}

func (e *TrackFallback) ReleasePad(instance *gst.Element, pad *gst.Pad) {
	self := gst.ToGstBin(instance)

	templ := pad.Template()
	if templ == nil || templ.GetName() != "sink_%u" {
		tname := "<nil>"
		if templ != nil {
			tname = templ.GetName()
		}
		self.Log(CAT, gst.LevelDebug, fmt.Sprintf("RequestNewPad called with unsupported template\ntemplate=%s", tname))
		return
	}

	name := pad.GetName()
	gpad := pad.AsGhostPad()
	if gpad == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Pad is not a ghost pad, cannot release\npad=%s", name))
		return
	}

	var session int
	if _, err := fmt.Sscanf(name, "sink_%d", &session); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to parse pad name\nname=%s\nerr=%v", name, err))
		return
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	kind := livekit.TrackSource(session)
	switch kind {
	case livekit.TrackSource_MICROPHONE,
		livekit.TrackSource_CAMERA,
		livekit.TrackSource_SCREEN_SHARE,
		livekit.TrackSource_SCREEN_SHARE_AUDIO:
	default:
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Unsupported session kind in pad name\nname=%s\nsession=%d\nkind=%s", name, session, kind.String()))
		return
	}

	target := gpad.GetTarget()
	if target == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Ghost pad has no target, cannot release\npad=%s", name))
		self.Error(fmt.Sprintf("Ghost pad %s has no target, cannot release", name), fmt.Errorf("ghost pad has no target"))
		return
	}

	if e.Tracks[kind].element == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Track element is nil while releasing pad\npad=%s", name))
		self.Error(fmt.Sprintf("Track element is nil while releasing pad %s", name), fmt.Errorf("track element is nil"))
		return
	}

	e.Tracks[kind].element.ReleaseRequestPad(target)

	if err := e.cleanupTrack(self, kind); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to cleanup track after releasing pad\npad=%s\nerr=%v", name, err))
		self.Error(fmt.Sprintf("Failed to cleanup track after releasing pad %s", name), err)
	}

	if !gpad.SetActive(false) {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to deactivate ghost pad\npad=%s", name))
		return
	}
	if !self.RemovePad(gpad.Pad) {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to remove ghost pad from SIP IO element\npad=%s", name))
		return
	}

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Successfully released pad for session\npad=%s\nsession=%d", name, session))
}

func (e *TrackFallback) initTrack(self *gst.Bin, kind livekit.TrackSource) error {
	if e.Tracks[kind].initialized {
		return nil
	}

	element, err := gst.NewElementWithProperties("fallbackswitch", map[string]interface{}{
		"timeout": uint64(20 * time.Second.Nanoseconds()),
	})
	if err != nil {
		return fmt.Errorf("failed to create %s fallback switch element: %w", kind.String(), err)
	}

	if err := self.Add(element); err != nil {
		return fmt.Errorf("failed to add %s fallback switch to bin: %w", kind.String(), err)
	}

	class := gst.ToElementClass(self.Class())
	gpad := gst.NewGhostPadFromTemplate(fmt.Sprintf("src_%d", kind), element.GetStaticPad("src"), class.GetPadTemplate("src_%u"))
	if gpad == nil {
		return fmt.Errorf("failed to create ghost pad for %s fallback", kind.String())
	}
	if !gpad.SetActive(true) {
		return fmt.Errorf("failed to activate ghost pad for %s fallback", kind.String())
	}
	if !self.AddPad(gpad.Pad) {
		return fmt.Errorf("failed to add ghost pad for %s fallback to bin", kind.String())
	}

	if !element.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to sync state of fallback switch with parent\nkind=%s", kind.String()))
	}

	e.Tracks[kind].element = element
	e.Tracks[kind].initialized = true

	self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Initialized fallback\nkind=%s", kind.String()))

	return nil
}

func (e *TrackFallback) cleanupTrack(self *gst.Bin, kind livekit.TrackSource) error {
	if !e.Tracks[kind].initialized || e.Tracks[kind].fallback != nil {
		return nil
	}

	sinks, err := e.Tracks[kind].element.GetSinkPads()
	if err != nil {
		return fmt.Errorf("failed to get sink pads of %s fallback switch: %w", kind.String(), err)
	}
	if len(sinks) > 0 {
		return nil
	}

	if err := e.Tracks[kind].element.SetState(gst.StateNull); err != nil {
		return fmt.Errorf("failed to set %s fallback switch state to null: %w", kind.String(), err)
	}

	if err := self.Remove(e.Tracks[kind].element); err != nil {
		return fmt.Errorf("failed to remove %s fallback switch from bin: %w", kind.String(), err)
	}

	if pad := self.GetStaticPad(fmt.Sprintf("src_%d", kind)); pad != nil {
		if !self.RemovePad(pad) {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to remove ghost pad for fallback from bin during cleanup\nkind=%s", kind.String()))
		}
	}

	e.Tracks[kind].element = nil
	e.Tracks[kind].initialized = false

	self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Cleaned up fallback\nkind=%s", kind.String()))

	return nil
}

func (e *TrackFallback) startTrackFallback(self *gst.Bin, kind livekit.TrackSource) {
	if err := e.initTrack(self, kind); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to initialize fallback\nkind=%s\nerr=%v", kind.String(), err))
		self.Error(fmt.Sprintf("Failed to initialize %s fallback", kind.String()), err)
		return
	}

	if e.Tracks[kind].fallback != nil {
		return
	}

	var fallback Fallback
	switch kind {
	case livekit.TrackSource_MICROPHONE, livekit.TrackSource_SCREEN_SHARE_AUDIO:
		fallback = &AudioFallback{}
	case livekit.TrackSource_CAMERA, livekit.TrackSource_SCREEN_SHARE:
		fallback = &VideoFallback{}
	default:
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Unsupported track source kind\nkind=%s", kind.String()))
		self.Error(fmt.Sprintf("Unsupported track source kind: %s", kind.String()), fmt.Errorf("unsupported track source kind"))
		return
	}

	src, err := fallback.Create(e, self)
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create fallback\nkind=%s\nerr=%v", kind.String(), err))
		self.Error(fmt.Sprintf("Failed to create %s fallback", kind.String()), err)
		return
	}

	sink := e.Tracks[kind].element.GetRequestPad("sink_%u")
	if sink == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get request pad for fallback\nkind=%s", kind.String()))
		self.Error(fmt.Sprintf("Failed to get request pad for %s fallback", kind.String()), err)
		return
	}

	if err := sink.SetProperty("priority", uint32(1)); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to set priority property on fallback pad\nkind=%s\nerr=%v", kind.String(), err))
		self.Error(fmt.Sprintf("Failed to set priority property on %s fallback pad", kind.String()), err)
		return
	}

	if ret := src.Link(sink); ret != gst.PadLinkOK {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link fallback src to switch sink\nkind=%s\nret=%s", kind.String(), ret.String()))
		self.Error(fmt.Sprintf("Failed to link %s fallback src to switch sink", kind.String()), err)
		return
	}

	if err := fallback.Sync(); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to sync fallback elements with parent\nkind=%s\nerr=%v", kind.String(), err))
	}

	e.Tracks[kind].fallback = fallback
	e.Tracks[kind].fallbackPad = sink

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Started fallback\nkind=%s", kind.String()))
}

func (e *TrackFallback) stopTrackFallback(self *gst.Bin, kind livekit.TrackSource) {
	if e.Tracks[kind].fallback == nil {
		return
	}

	if err := e.Tracks[kind].fallback.Remove(e, self); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to remove fallback\nkind=%s\nerr=%v", kind.String(), err))
		self.Error(fmt.Sprintf("Failed to remove %s fallback", kind.String()), err)
		return
	}

	if e.Tracks[kind].element != nil && e.Tracks[kind].fallbackPad != nil {
		e.Tracks[kind].element.ReleaseRequestPad(e.Tracks[kind].fallbackPad)
	}

	e.Tracks[kind].fallbackPad = nil
	e.Tracks[kind].fallback = nil

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Stopped fallback\nkind=%s", kind.String()))

	if err := e.cleanupTrack(self, kind); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to cleanup fallback\nkind=%s\nerr=%v", kind.String(), err))
		self.Error(fmt.Sprintf("Failed to cleanup %s fallback", kind.String()), err)
	}
}
