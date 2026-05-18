package h264rtppaybin

import (
	"fmt"
	"weak"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
)

var CAT = gst.NewDebugCategory(
	"h264rtppaybin",
	gst.DebugColorFgCyan,
	"H264 to RTP payloader bin with profile-level-id aware negotiation",
)

const (
	sinkCapsStr = "video/x-h264"
	srcCapsStr  = "application/x-rtp, media=(string)video, encoding-name=(string)H264"
)

// H264RtpPayBin wraps h264parse → profile_capsfilter → rtph264pay →
// h264rtpplidpatch. The plid patcher observes the downstream RTP caps
// during negotiation and emits a `plid-resolved` signal with the
// profile-level-id. The bin listens to that signal and programs
// profile_capsfilter with the matching `video/x-h264, profile=..., level=...`
// so the encoder upstream is caps-constrained to produce the bitstream
// the SDP advertises — independent of which H.264 encoder is used.
//
// Doing this off the plid patcher rather than in ChangeState is essential
// for factorybin-hosted pipelines: factorybin's SyncStateWithParent
// transitions the new element to its target state BEFORE setting the
// ghost pad targets, so a state-change peer-query would see ANY caps.
// plidPatch.TransformCaps on the other hand fires during caps
// negotiation, which runs only once the chain is actually wired.
//
// The bin also re-emits max-resolution(int, int) derived from the same
// plid, used by videoh264 to clamp its raw-video ScaleFilter.
type H264RtpPayBin struct {
	ProfileCapsFilter *gst.Element
	H264Parse         *gst.Element
	RtpH264Pay        *gst.Element
	PlidPatch         *gst.Element
}

func (e *H264RtpPayBin) New() glib.GoObjectSubclass {
	return &H264RtpPayBin{}
}

func (e *H264RtpPayBin) ClassInit(klass *glib.ObjectClass) {
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"H264 RTP Payloader Bin",
		"Codec/Payloader/Network/RTP",
		"H264 to RTP packetizer with profile-level-id aware caps negotiation",
		"Roomkit <roomkit-visio@numerique.gouv.fr>",
	)

	class.AddPadTemplate(gst.NewPadTemplate(
		"sink",
		gst.PadDirectionSink,
		gst.PadPresenceAlways,
		gst.NewCapsFromString(sinkCapsStr),
	))

	class.AddPadTemplate(gst.NewPadTemplate(
		"src",
		gst.PadDirectionSource,
		gst.PadPresenceAlways,
		gst.NewCapsFromString(srcCapsStr),
	))

	gst.SignalNew(
		class.Type(),
		"max-resolution",
		gst.SignalRunLast,
		glib.TYPE_NONE,
		glib.TYPE_INT, glib.TYPE_INT,
	)
}

func (e *H264RtpPayBin) Constructed(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	var err error

	wself := glib.WeakRefInit(self)
	ewaek := weak.Make(e)

	e.ProfileCapsFilter, err = gst.NewElementWithProperties("capsfilter", map[string]interface{}{})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create profile capsfilter: %v", err))
		self.Error("Failed to create profile capsfilter", err)
		return
	}
	if _, err := e.ProfileCapsFilter.GetStaticPad("src").Connect("notify::caps", func(pad *gst.Pad, _ *glib.ParamSpec) {
		self := gst.ToGstBin(wself.Get())
		e := ewaek.Value()
		if self == nil || e == nil {
			return
		}
		caps := pad.CurrentCaps()
		if caps == nil || caps.IsEmpty() {
			return
		}
		e.setMaxResolution(self, caps)
	}); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to connect to profile capsfilter caps notify: %v", err))
		self.Error("Failed to connect to profile capsfilter caps notify", err)
		return
	}

	e.H264Parse, err = gst.NewElementWithProperties("h264parse", map[string]interface{}{
		"config-interval": int(-1),
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create h264parse: %v", err))
		self.Error("Failed to create h264parse", err)
		return
	}

	e.RtpH264Pay, err = gst.NewElementWithProperties("rtph264pay", map[string]interface{}{
		"mtu":             int(1200),
		"config-interval": int(-1),
		"aggregate-mode":  int(1),
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create rtph264pay: %v", err))
		self.Error("Failed to create rtph264pay", err)
		return
	}

	e.PlidPatch, err = gst.NewElementWithProperties("h264rtpplidpatch", map[string]interface{}{})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create h264rtpplidpatch: %v", err))
		self.Error("Failed to create h264rtpplidpatch", err)
		return
	}

	if _, err := e.PlidPatch.Connect("plid-resolved", func(_ *gst.Element, plid string) {
		self := gst.ToGstBin(wself.Get())
		e := ewaek.Value()
		if self == nil || e == nil {
			return
		}
		e.onPlidResolved(self, plid)
	}); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to connect plid-resolved: %v", err))
		self.Error("Failed to connect plid-resolved", err)
		return
	}

	if err := self.AddMany(e.ProfileCapsFilter, e.H264Parse, e.RtpH264Pay, e.PlidPatch); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add elements to bin: %v", err))
		self.Error("Failed to add elements to bin", err)
		return
	}

	if err := gst.ElementLinkMany(e.ProfileCapsFilter, e.H264Parse, e.RtpH264Pay, e.PlidPatch); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link elements: %v", err))
		self.Error("Failed to link elements", err)
		return
	}

	elemClass := gst.ToElementClass(self.Class())

	ghostSink := gst.NewGhostPadFromTemplate("sink", e.ProfileCapsFilter.GetStaticPad("sink"), elemClass.GetPadTemplate("sink"))
	self.AddPad(ghostSink.Pad)

	ghostSrc := gst.NewGhostPadFromTemplate("src", e.PlidPatch.GetStaticPad("src"), elemClass.GetPadTemplate("src"))
	self.AddPad(ghostSrc.Pad)
}

func (e *H264RtpPayBin) setMaxResolution(self *gst.Bin, caps *gst.Caps) {
	caps = caps.Copy().Fixate()
	structure := caps.GetStructureAt(0)
	level, err := structure.GetString("level")
	if err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("No level field in caps %s: %v", caps.String(), err))
		return
	}
	levelIdc, is1b := gstH264LevelIDC(level)
	if levelIdc == 0 && !is1b {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Unknown level %q in caps %s", level, caps.String()))
		return
	}

	framerate := 24
	if framerateFracNum, framerateFracDen, err := structure.GetFraction("framerate"); err == nil {
		framerate = int(framerateFracNum / framerateFracDen)
	}

	maxWidth, maxHeight, ok := maxResolutionForLevel(levelIdc, is1b, framerate)
	if !ok {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to get max resolution for level %q in caps %s", level, caps.String()))
		return
	}
	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Caps changed: level=%s is1b=%t framerate=%d - %dx%d", level, is1b, framerate, maxWidth, maxHeight))
	if _, err := self.Emit("max-resolution", maxWidth, maxHeight); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to emit max-resolution: %v", err))
		self.Error("Failed to emit max-resolution", err)
	}
}

func (e *H264RtpPayBin) onPlidResolved(self *gst.Bin, plid string) {
	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Profile-level-id resolved: %s", plid))

	parsed, err := parseProfileLevelID(plid)
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to parse profile-level-id %q: %v", plid, err))
		self.Error(fmt.Sprintf("Failed to parse profile-level-id %q", plid), err)
		return
	}

	caps := gst.NewCapsFromString(h264CapsStringForPLID(parsed))
	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Parsed profile-level-id: profileIDC=%d profileIOP=%d levelIDC=%d isLevel1b=%t: %s", parsed.profileIDC, parsed.profileIOP, parsed.levelIDC, parsed.isLevel1b, caps.String()))
	if err := e.ProfileCapsFilter.SetProperty("caps", caps); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to set caps on profile capsfilter: %v", err))
		self.Error("Failed to set caps on profile capsfilter", err)
	}

	w, h, ok := maxResolutionForLevel(parsed.levelIDC, parsed.isLevel1b, 24)
	if !ok {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Unknown profile-level-id %q; cannot determine max resolution", plid))
		return
	}
	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Emitting max-resolution for level %q: %dx%d", plid, w, h))
	if _, err := self.Emit("max-resolution", w, h); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to emit max-resolution: %v", err))
		self.Error("Failed to emit max-resolution", err)
	}
}

func (e *H264RtpPayBin) Finalize(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	self.Log(CAT, gst.LevelDebug, "Finalizing H264RtpPayBin")

	e.H264Parse = nil
	e.ProfileCapsFilter = nil
	e.RtpH264Pay = nil
	e.PlidPatch = nil
}

func getProfileLevelID(st *gst.Structure) string {
	v, err := st.GetValue("profile-level-id")
	if err != nil {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
}
