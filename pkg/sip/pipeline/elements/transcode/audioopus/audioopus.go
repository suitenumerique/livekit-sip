package audioopus

import (
	"fmt"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
)

var CAT = gst.NewDebugCategory(
	"audio-opus",
	gst.DebugColorNone,
	"audio-opus Element",
)

const (
	UsageLivekit = "livekit"
	UsageSip     = "sip"
)

var properties = []*glib.ParamSpec{
	glib.NewStringParam(
		"usage",
		"Usage",
		"RTP leg the stream is sent on: livekit (PT 111, ssrc-audio-level extension) or sip (PT negotiated downstream, no extension)",
		nil,
		glib.ParameterWritable|glib.ParameterConstructOnly,
	),
}

type AudioOpus struct {
	usage string

	AudioConvert  *gst.Element
	AudioResample *gst.Element
	Level         *gst.Element
	OpusEnc       *gst.Element
	RtpOpusPay    *gst.Element
	RtpFilter     *gst.Element
}

func (e *AudioOpus) New() glib.GoObjectSubclass {
	return &AudioOpus{}
}

func (e *AudioOpus) ClassInit(klass *glib.ObjectClass) {
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"Audio to Opus Encoder",
		"Audio/Encoder",
		"Encodes raw audio to Opus RTP",
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
		gst.NewCapsFromString("application/x-rtp, media=(string)audio, clock-rate=(int)48000, encoding-name=(string)OPUS"),
	))

	class.InstallProperties(properties)
}

func (e *AudioOpus) InstanceInit(instance *glib.Object) {
	e.usage = UsageLivekit
}

func (e *AudioOpus) Constructed(instance *glib.Object) {
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

	e.Level, err = gst.NewElementWithProperties("level", map[string]interface{}{
		"audio-level-meta": true,
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create level element\nerr=%v", err))
		self.Error("Failed to create level element", err)
		return
	}

	e.OpusEnc, err = gst.NewElementWithProperties("opusenc", map[string]interface{}{
		"audio-type":             int(2048), // voice
		"frame-size":             int(20),
		"bitrate":                int(40000),
		"complexity":             int(8),
		"inband-fec":             true,
		"packet-loss-percentage": int(5),
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create opusenc element\nerr=%v", err))
		self.Error("Failed to create opusenc element", err)
		return
	}

	rtpPayProperties := map[string]interface{}{}
	rtpCaps := "application/x-rtp, media=(string)audio, clock-rate=(int)48000, encoding-name=(string)OPUS"
	if e.usage == UsageLivekit {
		rtpPayProperties["pt"] = 111
		rtpCaps += ", extmap-1=(string)< \"\", urn:ietf:params:rtp-hdrext:ssrc-audio-level, \"vad=on\" >"
	}

	e.RtpOpusPay, err = gst.NewElementWithProperties("rtpopuspay", rtpPayProperties)
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create rtpopuspay element\nerr=%v", err))
		self.Error("Failed to create rtpopuspay element", err)
		return
	}

	e.RtpFilter, err = gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString(rtpCaps),
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create capsfilter element\nerr=%v", err))
		self.Error("Failed to create capsfilter element", err)
		return
	}

	if err := self.AddMany(
		e.AudioConvert,
		e.AudioResample,
		e.Level,
		e.OpusEnc,
		e.RtpOpusPay,
		e.RtpFilter,
	); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add elements to bin\nerr=%v", err))
		self.Error("Failed to add elements to bin", err)
		return
	}

	if err := gst.ElementLinkMany(
		e.AudioConvert,
		e.AudioResample,
		e.Level,
		e.OpusEnc,
		e.RtpOpusPay,
		e.RtpFilter,
	); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link elements\nerr=%v", err))
		self.Error("Failed to link elements", err)
		return
	}

	elemClass := gst.ToElementClass(self.Class())

	ghostSink := gst.NewGhostPadFromTemplate("sink", e.AudioConvert.GetStaticPad("sink"), elemClass.GetPadTemplate("sink"))
	self.AddPad(ghostSink.Pad)

	ghostSrc := gst.NewGhostPadFromTemplate("src", e.RtpFilter.GetStaticPad("src"), elemClass.GetPadTemplate("src"))
	self.AddPad(ghostSrc.Pad)
}

func (e *AudioOpus) SetProperty(instance *glib.Object, id uint, value *glib.Value) {
	self := gst.ToGstBin(instance)
	param := properties[id]
	switch param.Name() {
	case "usage":
		gv, err := value.GoValue()
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error getting usage property value\nerr=%v", err))
			return
		}
		val, ok := gv.(string)
		if !ok {
			self.Log(CAT, gst.LevelError, "Invalid type for usage property")
			return
		}
		if val == "" {
			return
		}
		if val != UsageLivekit && val != UsageSip {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Invalid value for usage property\nvalue=%s", val))
			return
		}
		e.usage = val
	}
}

func (e *AudioOpus) Finalize(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	self.Log(CAT, gst.LevelDebug, "Finalizing AudioOpus element")

	e.AudioConvert = nil
	e.AudioResample = nil
	e.Level = nil
	e.OpusEnc = nil
	e.RtpOpusPay = nil
	e.RtpFilter = nil
}
