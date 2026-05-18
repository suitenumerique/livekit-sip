package h264rtppaybin

import (
	"fmt"
	"strconv"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/base"
)

type plidPatch struct {
	plid profileLevelID
}

func (e *plidPatch) New() glib.GoObjectSubclass { return &plidPatch{} }

func (e *plidPatch) ClassInit(klass *glib.ObjectClass) {
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"H264 RTP plid patcher",
		"Filter/Network/Video",
		"Strips profile-level-id on negotiation and stamps downstream's onto outcaps",
		"Roomkit <roomkit-visio@numerique.gouv.fr>",
	)

	caps := gst.NewCapsFromString(srcCapsStr)
	class.AddPadTemplate(gst.NewPadTemplate("sink", gst.PadDirectionSink, gst.PadPresenceAlways, caps))
	class.AddPadTemplate(gst.NewPadTemplate("src", gst.PadDirectionSource, gst.PadPresenceAlways, caps))

	gst.SignalNew(
		class.Type(),
		"plid-resolved",
		gst.SignalRunLast,
		glib.TYPE_NONE,
		glib.TYPE_STRING,
	)
}

func (e *plidPatch) InstanceInit(instance *glib.Object) {
	self := base.ToGstBaseTransform(instance)
	self.SetPassthrough(true)
	self.SetPreferPassthrough(false)
}

func (e *plidPatch) TransformCaps(self *base.GstBaseTransform, direction gst.PadDirection, caps, filter *gst.Caps) *gst.Caps {
	if direction == gst.PadDirectionSource {
		e.resolvePlid(self)
	}

	result := caps.Copy()
	for i := 0; i < result.GetSize(); i++ {
		st := result.GetStructureAt(i)
		st.RemoveValue("profile-level-id")
	}
	if filter != nil {
		result = result.Intersect(filter)
	}
	return result.Ref()
}

func (e *plidPatch) resolvePlid(self *base.GstBaseTransform) {
	downstream := self.SrcPad().PeerQueryCaps(nil)
	if downstream == nil || downstream.IsEmpty() || downstream.IsAny() || downstream.GetSize() == 0 {
		return
	}
	st := downstream.GetStructureAt(0)
	plid, err := st.GetString("profile-level-id")
	if err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("resolvePlid: downstream caps missing profile-level-id: %v", err))
		return
	}

	parsed, err := parseProfileLevelID(plid)
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("resolvePlid: failed to parse profile-level-id %q: %v", plid, err))
		return
	}

	var maxFs, maxMbps int

	maxFsStr, err := st.GetString("max-fs")
	if err == nil {
		maxFs, err = strconv.Atoi(maxFsStr)
		if err != nil {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Invalid max-fs value in downstream caps: %v", err))
			maxFs = 0
		}
	}

	maxMbpsStr, err := st.GetString("max-mbps")
	if err == nil {
		maxMbps, err = strconv.Atoi(maxMbpsStr)
		if err != nil {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Invalid max-mbps value in downstream caps: %v", err))
			maxMbps = 0
		}
	}

	patched := patchProfileLevelID(parsed, maxFs, maxMbps)

	if patched == e.plid {
		self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Profile-level-id already resolved and unchanged: %s", patched))
		return
	}

	e.plid = patched

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Profile-level-id resolved: %s -> %s level %d", plid, patched, patched.levelIDC))

	if _, err := self.Element.Emit("plid-resolved", patched.String()); err != nil {
		self.Log(CAT, gst.LevelError, "failed to emit plid-resolved: "+err.Error())
	}
}

func (e *plidPatch) SetCaps(self *base.GstBaseTransform, incaps, outcaps *gst.Caps) bool {
	downstream := self.SrcPad().PeerQueryCaps(nil)
	if downstream == nil || downstream.IsEmpty() || downstream.IsAny() || downstream.GetSize() == 0 {
		return true
	}

	plid := getProfileLevelID(downstream.GetStructureAt(0))
	if plid == "" {
		return true
	}

	if outcaps.GetSize() > 0 {
		st := outcaps.GetStructureAt(0)
		_ = st.SetValue("profile-level-id", plid)
	}
	return true
}
