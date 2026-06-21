package opusaudio

import (
	"fmt"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
)

var CAT = gst.NewDebugCategory(
	"opus-audio",
	gst.DebugColorNone,
	"opus-audio Element",
)

type OpusAudio struct {
	RtpOpusDepay  *gst.Element
	OpusDec       *gst.Element
	AudioConvert  *gst.Element
	AudioResample *gst.Element
	AudioRate     *gst.Element
}

func (e *OpusAudio) New() glib.GoObjectSubclass {
	return &OpusAudio{}
}

func (e *OpusAudio) ClassInit(klass *glib.ObjectClass) {
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"Opus to Audio Decoder",
		"Audio/Decoder",
		"Decodes Opus RTP to raw audio",
		"Roomkit <roomkit-visio@numerique.gouv.fr>",
	)

	class.AddPadTemplate(gst.NewPadTemplate(
		"sink",
		gst.PadDirectionSink,
		gst.PadPresenceAlways,
		gst.NewCapsFromString("application/x-rtp, media=(string)audio, clock-rate=(int)48000, encoding-name=(string)OPUS"),
	))

	class.AddPadTemplate(gst.NewPadTemplate(
		"src",
		gst.PadDirectionSource,
		gst.PadPresenceAlways,
		gst.NewCapsFromString("audio/x-raw"),
	))
}

func (e *OpusAudio) InstanceInit(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	var err error

	e.RtpOpusDepay, err = gst.NewElement("rtpopusdepay")
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create rtpopusdepay element\nerr=%v", err))
		self.Error("Failed to create rtpopusdepay element", err)
		return
	}

	e.OpusDec, err = gst.NewElement("opusdec")
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create opusdec element\nerr=%v", err))
		self.Error("Failed to create opusdec element", err)
		return
	}

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

	e.AudioRate, err = gst.NewElement("audiorate")
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create audiorate element\nerr=%v", err))
		self.Error("Failed to create audiorate element", err)
		return
	}

	if err := self.AddMany(
		e.RtpOpusDepay,
		e.OpusDec,
		e.AudioConvert,
		e.AudioResample,
		e.AudioRate,
	); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add elements to bin\nerr=%v", err))
		self.Error("Failed to add elements to bin", err)
		return
	}

	if err := gst.ElementLinkMany(
		e.RtpOpusDepay,
		e.OpusDec,
		e.AudioConvert,
		e.AudioResample,
		e.AudioRate,
	); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link elements\nerr=%v", err))
		self.Error("Failed to link elements", err)
		return
	}

	elemClass := gst.ToElementClass(self.Class())

	ghostSink := gst.NewGhostPadFromTemplate("sink", e.RtpOpusDepay.GetStaticPad("sink"), elemClass.GetPadTemplate("sink"))
	self.AddPad(ghostSink.Pad)

	ghostSrc := gst.NewGhostPadFromTemplate("src", e.AudioRate.GetStaticPad("src"), elemClass.GetPadTemplate("src"))
	self.AddPad(ghostSrc.Pad)
}

func (e *OpusAudio) Finalize(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	self.Log(CAT, gst.LevelDebug, "Finalizing OpusAudio element")

	e.RtpOpusDepay = nil
	e.OpusDec = nil
	e.AudioConvert = nil
	e.AudioResample = nil
	e.AudioRate = nil
}
