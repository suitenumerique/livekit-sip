package pcmaaudio

import (
	"github.com/go-gst/go-gst/gst"
)

func Register() bool {
	return gst.RegisterElement(
		nil,
		"pcma-audio",
		gst.RankNone,
		&PcmaAudio{},
		gst.ExtendsBin,
	)
}
