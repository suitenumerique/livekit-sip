package opusaudio

import (
	"github.com/go-gst/go-gst/gst"
)

func Register() bool {
	return gst.RegisterElement(
		nil,
		"opus-audio",
		gst.RankNone,
		&OpusAudio{},
		gst.ExtendsBin,
	)
}
