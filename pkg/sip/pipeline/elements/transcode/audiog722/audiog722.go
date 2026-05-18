package audiog722

import (
	"fmt"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
)

var CAT = gst.NewDebugCategory(
	"audio-g722",
	gst.DebugColorNone,
	"audio-g722 Element",
)

type AudioG722 struct {
	AudioConvert  *gst.Element
	AudioResample *gst.Element
	G722Enc       *gst.Element
	RtpG722Pay    *gst.Element
}

func (e *AudioG722) New() glib.GoObjectSubclass {
	return &AudioG722{}
}

func (e *AudioG722) ClassInit(klass *glib.ObjectClass) {
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"Audio to G722 Encoder",
		"Audio/Encoder",
		"Encodes raw audio to G722 RTP",
		"Roomkit <roomkit-visio@numerique.gouv.fr>",
	)

	class.AddPadTemplate(gst.NewPadTemplate(
		"sink",
		gst.PadDirectionSink,
		gst.PadPresenceAlways,
		gst.NewCapsFromString("audio/x-raw"),
	))

	class.AddPadTemplate(gst.NewPadTemplate(
		"src",
		gst.PadDirectionSource,
		gst.PadPresenceAlways,
		gst.NewCapsFromString("application/x-rtp, media=(string)audio, clock-rate=(int)8000, encoding-name=(string)G722"),
	))
}

func (e *AudioG722) InstanceInit(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	var err error

	e.AudioConvert, err = gst.NewElement("audioconvert")
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create audioconvert element: %v", err))
		self.Error("Failed to create audioconvert element", err)
		return
	}

	e.AudioResample, err = gst.NewElement("audioresample")
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create audioresample element: %v", err))
		self.Error("Failed to create audioresample element", err)
		return
	}

	e.G722Enc, err = gst.NewElementWithProperties("avenc_g722", map[string]interface{}{})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create avenc_g722 element: %v", err))
		self.Error("Failed to create avenc_g722 element", err)
		return
	}

	e.RtpG722Pay, err = gst.NewElementWithProperties("rtpg722pay", map[string]interface{}{})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create rtpg722pay element: %v", err))
		self.Error("Failed to create rtpg722pay element", err)
		return
	}

	if err := self.AddMany(
		e.AudioConvert,
		e.AudioResample,
		e.G722Enc,
		e.RtpG722Pay,
	); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add elements to bin: %v", err))
		self.Error("Failed to add elements to bin", err)
		return
	}

	if err := gst.ElementLinkMany(
		e.AudioConvert,
		e.AudioResample,
		e.G722Enc,
		e.RtpG722Pay,
	); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link elements: %v", err))
		self.Error("Failed to link elements", err)
		return
	}

	elemClass := gst.ToElementClass(self.Class())

	ghostSink := gst.NewGhostPadFromTemplate("sink", e.AudioConvert.GetStaticPad("sink"), elemClass.GetPadTemplate("sink"))
	self.AddPad(ghostSink.Pad)

	ghostSrc := gst.NewGhostPadFromTemplate("src", e.RtpG722Pay.GetStaticPad("src"), elemClass.GetPadTemplate("src"))
	self.AddPad(ghostSrc.Pad)
}

func (e *AudioG722) Finalize(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	self.Log(CAT, gst.LevelDebug, "Finalizing AudioG722 element")

	e.AudioConvert = nil
	e.AudioResample = nil
	e.G722Enc = nil
	e.RtpG722Pay = nil
}
