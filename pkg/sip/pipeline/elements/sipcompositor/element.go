package sipcompositor

import (
	"fmt"
	"sync"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/livekit"
)

var CAT = gst.NewDebugCategory(
	"sip_compositor",
	gst.DebugColorFgYellow,
	"sip_compositor Element",
)

type SipCompositor struct {
	mu sync.Mutex

	videoWidth     uint
	videoHeight    uint
	videoFramerate uint

	*SipCompositorMicrophone
	*SipCompositorCamera
	*SipCompositorScreenshare
}

func (e *SipCompositor) New() glib.GoObjectSubclass {
	return &SipCompositor{}
}

func (e *SipCompositor) ClassInit(klass *glib.ObjectClass) {
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"SIP Compositor",
		"Transform",
		"Element to composite SIP audio tracks and pass through a single video track",
		"Roomkit <roomkit-visio@numerique.gouv.fr>",
	)

	class.AddPadTemplate(gst.NewPadTemplate(
		"sink_%u_%u_%u",
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

func (e *SipCompositor) InstanceInit(instance *glib.Object) {
	e.videoWidth = 1280
	e.videoHeight = 720
	e.videoFramerate = 24
}

func (e *SipCompositor) Finalize(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	self.Log(CAT, gst.LevelDebug, "Finalizing SipCompositor element")

	e.mu.Lock()
	defer e.mu.Unlock()

	e.SipCompositorCamera = nil
	e.SipCompositorMicrophone = nil
	e.SipCompositorScreenshare = nil
}

func (e *SipCompositor) RequestNewPad(instance *gst.Element, templ *gst.PadTemplate, name string, caps *gst.Caps) *gst.Pad {
	self := gst.ToGstBin(instance)

	if templ == nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Unknown pad template for requested pad\nname=%s", name))
		return nil
	}

	if templ.Name() != "sink_%u_%u_%u" {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("No handler for requested pad template\ntemplate=%s", templ.Name()))
		return nil
	}

	return e.requestNewSinkPad(self, templ, name)
}

func (e *SipCompositor) requestNewSinkPad(self *gst.Bin, templ *gst.PadTemplate, name string) *gst.Pad {
	if name == "" {
		self.Log(CAT, gst.LevelWarning, "Requested pad with empty name")
		return nil
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	var session, ssrc, payload int
	if _, err := fmt.Sscanf(name, "sink_%d_%d_%d", &session, &ssrc, &payload); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Invalid pad name\nname=%s", name))
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
	default:
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Unknown track source in pad name\nname=%s", name))
		return nil
	}

	if pad == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create pad for name\nname=%s", name))
		return nil
	}

	return pad
}

func (e *SipCompositor) ReleasePad(instance *gst.Element, pad *gst.Pad) {
	self := gst.ToGstBin(instance)

	gpad := pad.AsGhostPad()
	if gpad == nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Released pad is not a ghost pad\npad=%s", pad.GetName()))
		return
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	var session, ssrc, pt int
	if _, err := fmt.Sscanf(gpad.GetName(), "sink_%d_%d_%d", &session, &ssrc, &pt); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Invalid pad name on release\npad=%s", gpad.GetName()))
		return
	}

	kind := livekit.TrackSource(session)
	switch kind {
	case livekit.TrackSource_MICROPHONE:
		e.releaseMicrophoneSinkPad(self, gpad)
	case livekit.TrackSource_CAMERA:
		e.releaseCameraSinkPad(self, gpad)
	case livekit.TrackSource_SCREEN_SHARE:
		e.releaseScreenshareSinkPad(self, gpad)
	default:
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Unknown track source in released pad name\npad=%s", gpad.GetName()))
		return
	}
}
