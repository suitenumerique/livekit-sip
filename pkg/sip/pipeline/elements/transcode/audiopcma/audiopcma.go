package audiopcma

import (
	"fmt"
	"time"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
)

var CAT = gst.NewDebugCategory(
	"audio-pcma",
	gst.DebugColorNone,
	"audio-pcma Element",
)

type AudioPcma struct {
	AudioConvert  *gst.Element
	AudioResample *gst.Element
	ALawEnc       *gst.Element
	RtpPcmaPay    *gst.Element
}

func (e *AudioPcma) New() glib.GoObjectSubclass {
	return &AudioPcma{}
}

func (e *AudioPcma) ClassInit(klass *glib.ObjectClass) {
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"Audio to PCMA Encoder",
		"Audio/Encoder",
		"Encodes raw audio to PCMA RTP",
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
		gst.NewCapsFromString("application/x-rtp, media=(string)audio, clock-rate=(int)8000, encoding-name=(string)PCMA"),
	))
}

func (e *AudioPcma) InstanceInit(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	var err error

	e.AudioConvert, err = gst.NewElement("audioconvert")
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create audioconvert element\nerr=%v", err))
		self.Error("Failed to create audioconvert element", err)
		return
	}

	e.AudioResample, err = gst.NewElement("audioresample")
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create audioresample element\nerr=%v", err))
		self.Error("Failed to create audioresample element", err)
		return
	}

	e.ALawEnc, err = gst.NewElementWithProperties("alawenc", map[string]interface{}{})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create alawenc element\nerr=%v", err))
		self.Error("Failed to create alawenc element", err)
		return
	}

	e.RtpPcmaPay, err = gst.NewElementWithProperties("rtppcmapay", map[string]interface{}{
		"min-ptime":      int64(20 * time.Millisecond.Nanoseconds()),
		"max-ptime":      int64(20 * time.Millisecond.Nanoseconds()),
		"ptime-multiple": int64(20 * time.Millisecond.Nanoseconds()),
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create rtppcmapay element\nerr=%v", err))
		self.Error("Failed to create rtppcmapay element", err)
		return
	}

	if err := self.AddMany(
		e.AudioConvert,
		e.AudioResample,
		e.ALawEnc,
		e.RtpPcmaPay,
	); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add elements to bin\nerr=%v", err))
		self.Error("Failed to add elements to bin", err)
		return
	}

	if err := gst.ElementLinkMany(
		e.AudioConvert,
		e.AudioResample,
		e.ALawEnc,
		e.RtpPcmaPay,
	); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link elements\nerr=%v", err))
		self.Error("Failed to link elements", err)
		return
	}

	elemClass := gst.ToElementClass(self.Class())

	ghostSink := gst.NewGhostPadFromTemplate("sink", e.AudioConvert.GetStaticPad("sink"), elemClass.GetPadTemplate("sink"))
	self.AddPad(ghostSink.Pad)

	ghostSrc := gst.NewGhostPadFromTemplate("src", e.RtpPcmaPay.GetStaticPad("src"), elemClass.GetPadTemplate("src"))
	self.AddPad(ghostSrc.Pad)
}

func (e *AudioPcma) Finalize(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	self.Log(CAT, gst.LevelDebug, "Finalizing AudioPCMA element")

	e.AudioConvert = nil
	e.AudioResample = nil
	e.ALawEnc = nil
	e.RtpPcmaPay = nil
}
