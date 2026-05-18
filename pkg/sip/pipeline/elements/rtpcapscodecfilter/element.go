package rtpcapscodecfilter

import (
	"fmt"
	"runtime"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/base"
)

var CAT = gst.NewDebugCategory(
	"rtpcapscodecfilter",
	gst.DebugColorNone,
	"RTP caps codec filtering",
)

var properties = []*glib.ParamSpec{
	glib.NewBoxedParam(
		"caps",
		"Target Caps",
		"The target caps to filter against",
		gst.TypeCaps,
		glib.ParameterWritable|glib.ParameterConstructOnly,
	),
}

type RtpCapsCodecFilter struct {
	filter *gst.Caps
}

func (e *RtpCapsCodecFilter) New() glib.GoObjectSubclass {
	return &RtpCapsCodecFilter{}
}

func (e *RtpCapsCodecFilter) ClassInit(klass *glib.ObjectClass) {
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"RTP Caps Codec Filter",
		"Filter",
		"Performs codec filtering on application/x-rtp caps",
		"Roomkit <roomkit-visio@numerique.gouv.fr>",
	)

	class.AddPadTemplate(gst.NewPadTemplate(
		"sink",
		gst.PadDirectionSink,
		gst.PadPresenceAlways,
		gst.NewCapsFromString("application/x-rtp"),
	))

	class.AddPadTemplate(gst.NewPadTemplate(
		"src",
		gst.PadDirectionSource,
		gst.PadPresenceAlways,
		gst.NewCapsFromString("application/x-rtp"),
	))

	class.InstallProperties(properties)
}

func (e *RtpCapsCodecFilter) InstanceInit(instance *glib.Object) {
	self := base.ToGstBaseTransform(instance)
	self.SetPassthrough(true)
	e.filter = gst.NewAnyCaps()
}

func (e *RtpCapsCodecFilter) SetProperty(instance *glib.Object, id uint, value *glib.Value) {
	self := base.ToGstBaseTransform(instance)
	param := properties[id]
	switch param.Name() {
	case "caps":
		gv, err := value.GoValue()
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error getting caps property value: %v", err))
			self.Error("Error getting caps property value", err)
			return
		}
		val, ok := gv.(*gst.Caps)
		if !ok {
			self.Log(CAT, gst.LevelError, "Invalid type for caps property")
			self.Error("Invalid type for caps property", fmt.Errorf("expected *gst.Caps, got %T", gv))
			return
		}
		if val != nil {
			e.filter = val.Copy()
		}
	}
}

func (e *RtpCapsCodecFilter) TransformCaps(self *base.GstBaseTransform, direction gst.PadDirection, caps, filter *gst.Caps) *gst.Caps {
	var result *gst.Caps

	if direction == gst.PadDirectionSource || caps.IsAny() {
		result = caps.Copy()
	} else {
		for i := 0; i < caps.GetSize(); i++ {
			st := caps.GetStructureAt(i).Copy()
			runtime.SetFinalizer(st, nil)
			candidate := gst.NewFullCaps(st)
			if candidate.CanIntersect(e.filter) {
				result = candidate
				break
			}
		}
	}

	if result == nil {
		self.Log(CAT, gst.LevelWarning, "No compatible caps found in target for input caps")
		result = gst.NewEmptyCaps()
	}

	if filter != nil {
		result = result.Intersect(filter)
	}

	self.Log(CAT, gst.LevelDebug, fmt.Sprintf("transform_caps dir=%d: %s", direction, result))
	return result.Ref()
}
