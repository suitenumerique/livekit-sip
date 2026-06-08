package livekitcompositor

import (
	"context"
	"fmt"
	"sync"
	"weak"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/livekitbin/livekittracks"
	"github.com/samber/lo"
)

var CAT = gst.NewDebugCategory(
	"livekit_compositor",
	gst.DebugColorFgYellow,
	"livekit_compositor Element",
)

const NbTracks = int(livekit.TrackSource_SCREEN_SHARE_AUDIO) + 1

type LivekitCompositor struct {
	mu     sync.Mutex
	ctx    context.Context
	cancel context.CancelFunc

	videoWidth     uint
	videoHeight    uint
	videoFramerate uint
	lang           string
	microphone     bool
	camera         bool
	screenshare    bool

	*LivekitCompositorMicrophone
	*LivekitCompositorCamera
	*LivekitCompositorScreenshare

	participants map[string]livekittracks.ParticipantInfo           // key is participant SID
	tracks       [NbTracks]map[string]livekittracks.TrackSourceInfo // key is participant SID, indexed by livekit.TrackSource

	currentLayout []string

	overlayMessage overlayMessage
}

func (e *LivekitCompositor) New() glib.GoObjectSubclass {
	return &LivekitCompositor{}
}

func (e *LivekitCompositor) ClassInit(klass *glib.ObjectClass) {
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"LiveKit Compositor",
		"Transform",
		"Element to composite multiple LiveKit tracks into a single video stream",
		"Roomkit <roomkit-visio@numerique.gouv.fr>",
	)

	gst.SignalNew(
		class.Type(),
		"active-speakers-changed",
		gst.SignalRunLast,
		glib.TYPE_NONE,
		gst.TypeStructure,
	)

	gst.SignalNew(
		class.Type(),
		"participant-join",
		gst.SignalRunLast,
		glib.TYPE_NONE,
		gst.TypeStructure, // livekittracks.ParticipantInfo
	)

	gst.SignalNew(
		class.Type(),
		"participant-left",
		gst.SignalRunLast,
		glib.TYPE_NONE,
		gst.TypeStructure, // livekittracks.ParticipantInfo
	)

	class.AddPadTemplate(gst.NewPadTemplate(
		"sink_%u_%u_%u",
		gst.PadDirectionSink,
		gst.PadPresenceRequest,
		gst.NewAnyCaps(),
	))

	class.AddPadTemplate(gst.NewPadTemplate(
		"raw_sink_%u",
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

func (e *LivekitCompositor) InstanceInit(instance *glib.Object) {
	e.ctx, e.cancel = context.WithCancel(context.Background())

	e.participants = make(map[string]livekittracks.ParticipantInfo)
	for i := 0; i < NbTracks; i++ {
		e.tracks[i] = make(map[string]livekittracks.TrackSourceInfo)
	}
	e.videoWidth = 1280
	e.videoHeight = 720
	e.videoFramerate = 24
	e.lang = "en"
}

func (e *LivekitCompositor) Constructed(instance *glib.Object) {
	self := gst.ToGstBin(instance)

	eweak := weak.Make(e)
	if _, err := self.Connect("active-speakers-changed", func(instance *gst.Element, structure *gst.Structure) {
		ptr := eweak.Value()
		if ptr == nil {
			return
		}
		ptr.onActiveSpeakersChanged(instance, structure)
	}); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to connect active-speakers-changed signal: %v", err))
		self.Error("Failed to connect active-speakers-changed signal", err)
	}

	if _, err := self.Connect("participant-join", func(instance *gst.Element, structure *gst.Structure) {
		ptr := eweak.Value()
		if ptr == nil {
			return
		}
		ptr.onParticipantJoin(instance, structure)
	}); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to connect participant-join signal: %v", err))
		self.Error("Failed to connect participant-join signal", err)
	}

	if _, err := self.Connect("participant-left", func(instance *gst.Element, structure *gst.Structure) {
		ptr := eweak.Value()
		if ptr == nil {
			return
		}
		ptr.onParticipantLeft(instance, structure)
	}); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to connect participant-left signal: %v", err))
		self.Error("Failed to connect participant-left signal", err)
	}
}

func (e *LivekitCompositor) RequestNewPad(instance *gst.Element, templ *gst.PadTemplate, name string, caps *gst.Caps) *gst.Pad {
	self := gst.ToGstBin(instance)

	if templ == nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Unknown pad template for requested pad: %s", name))
		return nil
	}

	switch templ.Name() {
	case "sink_%u_%u_%u":
		return e.requestNewSinkPad(self, templ, name)
	case "raw_sink_%u":
		return e.requestNewRawSinkPad(self, templ, name)
	default:
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("No handler for requested pad template: %s", templ.Name()))
		return nil
	}
}

func (e *LivekitCompositor) requestNewSinkPad(self *gst.Bin, templ *gst.PadTemplate, name string) *gst.Pad {
	if name == "" {
		self.Log(CAT, gst.LevelWarning, "Requested pad with empty name")
		return nil
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	var session, ssrc, payload int
	if _, err := fmt.Sscanf(name, "sink_%d_%d_%d", &session, &ssrc, &payload); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Invalid pad name: %s", name))
		return nil
	}

	var pad *gst.Pad
	kind := livekit.TrackSource(session)
	switch kind {
	case livekit.TrackSource_MICROPHONE:
		pad = e.requestNewMicrophoneSinkPad(self, templ, name)
	case livekit.TrackSource_CAMERA:
		pad = e.requestNewCameraSinkPad(self, templ, name)
	case livekit.TrackSource_SCREEN_SHARE:
		pad = e.requestNewScreenshareSinkPad(self, templ, name)
	case livekit.TrackSource_SCREEN_SHARE_AUDIO:
		pad = e.requestNewScreenShareAudioSinkPad(self, templ, name)
	default:
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Unknown track source in pad name: %s", name))
		return nil
	}

	if pad == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create pad for name: %s", name))
		return nil
	}

	wself := glib.WeakRefInit(self)
	eweak := weak.Make(e)

	livekittracks.PadOnTrackSourceInfo(pad, func(pad *gst.Pad, info livekittracks.TrackSourceInfo) {
		e := eweak.Value()
		if e == nil {
			return
		}
		self := gst.ToGstBin(wself.Get())
		if self == nil || self.Instance() == nil {
			return
		}
		e.mu.Lock()
		defer e.mu.Unlock()

		var session, ssrc, pt int
		if _, err := fmt.Sscanf(pad.GetName(), "sink_%d_%d_%d", &session, &ssrc, &pt); err != nil {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Invalid pad name in track source info callback: %s", pad.GetName()))
			return
		}

		if _, exist := e.participants[info.ParticipantSID]; !exist {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Participant SID from track source info not found in participants map: %s", info.ParticipantSID))
			return
		}

		if ssrc != int(info.SSRC) || session != int(info.Source) || pt != int(info.PT) {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Track source info does not match pad name: %s (info: session=%d, ssrc=%d, pt=%d)", pad.GetName(), info.Source, info.SSRC, info.PT))
			info.SSRC = uint(ssrc)
			info.Source = livekit.TrackSource(kind)
			info.PT = uint(pt)
		}
		e.tracks[info.Source][info.ParticipantSID] = info

		if lo.Contains(e.currentLayout, info.ParticipantSID) {
			e.applyCameraLayout(self, e.currentLayout)
			e.applyMicrophoneLayout(self, e.currentLayout)
			e.applyScreenshareLayout(self, e.currentLayout)
		}
	})

	return pad
}

func (e *LivekitCompositor) ReleasePad(instance *gst.Element, pad *gst.Pad) {
	self := gst.ToGstBin(instance)

	gpad := pad.AsGhostPad()
	if gpad == nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Released pad is not a ghost pad: %s", pad.GetName()))
		return
	}

	templ := gpad.Template()
	if templ == nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Released pad has no template: %s", pad.GetName()))
		return
	}

	switch templ.Name() {
	case "sink_%u_%u_%u":
		e.releaseSinkPad(self, gpad)
	case "raw_sink_%u":
		e.releaseRawSinkPad(self, gpad)
	default:
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("No handler for released pad template: %s", templ.Name()))
		return
	}
}

func (e *LivekitCompositor) releaseSinkPad(self *gst.Bin, gpad *gst.GhostPad) {
	e.mu.Lock()
	defer e.mu.Unlock()

	var session, ssrc, pt int
	if _, err := fmt.Sscanf(gpad.GetName(), "sink_%d_%d_%d", &session, &ssrc, &pt); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Invalid pad name on release: %s", gpad.GetName()))
		return
	}

	info, infoErr := livekittracks.PadGetTrackSourceInfo(gpad.Pad)

	kind := livekit.TrackSource(session)
	switch kind {
	case livekit.TrackSource_MICROPHONE:
		e.releaseMicrophoneSinkPad(self, gpad)
	case livekit.TrackSource_CAMERA:
		e.releaseCameraSinkPad(self, gpad)
	case livekit.TrackSource_SCREEN_SHARE:
		e.releaseScreenshareSinkPad(self, gpad)
	case livekit.TrackSource_SCREEN_SHARE_AUDIO:
		e.releaseScreenShareAudioSinkPad(self, gpad)
	default:
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Unknown track source in released pad name: %s", gpad.GetName()))
		return
	}

	if infoErr != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to get track source info for released pad: %s, error: %v", gpad.GetName(), infoErr))
		return
	}

	if _, exist := e.participants[info.ParticipantSID]; !exist {
		return
	}

	delete(e.tracks[info.Source], info.ParticipantSID)
}

func (e *LivekitCompositor) Finalize(instance *glib.Object) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.cancel()

	e.participants = nil
	for i := 0; i < NbTracks; i++ {
		e.tracks[i] = nil
	}
	e.currentLayout = nil
	e.LivekitCompositorCamera = nil
	e.LivekitCompositorMicrophone = nil
	e.LivekitCompositorScreenshare = nil
}
