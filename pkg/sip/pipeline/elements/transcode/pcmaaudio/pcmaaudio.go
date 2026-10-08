package pcmaaudio

import (
	"fmt"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"

	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/audioplc"
)

var CAT = gst.NewDebugCategory(
	"pcma-audio",
	gst.DebugColorNone,
	"pcma-audio Element",
)

type PcmaAudio struct {
	RtpPcmaDepay  *gst.Element
	ALawDec       *gst.Element
	Plc           *gst.Element
	plcStats      *audioplc.Stats
	AudioConvert  *gst.Element
	AudioResample *gst.Element
}

func (e *PcmaAudio) New() glib.GoObjectSubclass {
	return &PcmaAudio{}
}

func (e *PcmaAudio) ClassInit(klass *glib.ObjectClass) {
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"PCMA to Audio Decoder",
		"Audio/Decoder",
		"Decodes PCMA RTP to raw audio",
		"Roomkit <roomkit-visio@numerique.gouv.fr>",
	)

	class.AddPadTemplate(gst.NewPadTemplate(
		"sink",
		gst.PadDirectionSink,
		gst.PadPresenceAlways,
		gst.NewCapsFromString("application/x-rtp, media=(string)audio, clock-rate=(int)8000, encoding-name=(string)PCMA"),
	))

	class.AddPadTemplate(gst.NewPadTemplate(
		"src",
		gst.PadDirectionSource,
		gst.PadPresenceAlways,
		gst.NewCapsFromString("audio/x-raw"),
	))
}

func (e *PcmaAudio) InstanceInit(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	var err error

	e.RtpPcmaDepay, err = gst.NewElement("rtppcmadepay")
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create rtppcmadepay element\nerr=%v", err))
		self.Error("Failed to create rtppcmadepay element", err)
		return
	}

	e.ALawDec, err = gst.NewElement("alawdec")
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create alawdec element\nerr=%v", err))
		self.Error("Failed to create alawdec element", err)
		return
	}

	e.Plc, e.plcStats, err = audioplc.New()
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create audio packet loss concealment\nerr=%v", err))
		self.Error("Failed to create audio packet loss concealment", err)
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

	if err := self.AddMany(
		e.RtpPcmaDepay,
		e.ALawDec,
		e.Plc,
		e.AudioConvert,
		e.AudioResample,
	); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add elements to bin\nerr=%v", err))
		self.Error("Failed to add elements to bin", err)
		return
	}

	if err := gst.ElementLinkMany(
		e.RtpPcmaDepay,
		e.ALawDec,
		e.Plc,
		e.AudioConvert,
		e.AudioResample,
	); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link elements\nerr=%v", err))
		self.Error("Failed to link elements", err)
		return
	}

	elemClass := gst.ToElementClass(self.Class())

	ghostSink := gst.NewGhostPadFromTemplate("sink", e.RtpPcmaDepay.GetStaticPad("sink"), elemClass.GetPadTemplate("sink"))
	self.AddPad(ghostSink.Pad)

	ghostSrc := gst.NewGhostPadFromTemplate("src", e.AudioResample.GetStaticPad("src"), elemClass.GetPadTemplate("src"))
	self.AddPad(ghostSrc.Pad)
}

func (e *PcmaAudio) Finalize(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	self.Log(CAT, gst.LevelDebug, "Finalizing PcmaAudio element")
	if gaps, concealed := e.plcStats.Summary(); gaps > 0 {
		self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Concealed audio packet losses\ngaps=%d\nconcealed_ms=%d", gaps, concealed.Milliseconds()))
	}

	e.RtpPcmaDepay = nil
	e.ALawDec = nil
	e.Plc = nil
	e.AudioConvert = nil
	e.AudioResample = nil
}
