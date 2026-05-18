package dtmfaudio

import (
	"github.com/go-gst/go-gst/gst"
)

func Register() bool {
	return gst.RegisterElement(
		nil,
		"dtmf-audio",
		gst.RankNone,
		&DtmfAudio{},
		gst.ExtendsBin,
	)
}
