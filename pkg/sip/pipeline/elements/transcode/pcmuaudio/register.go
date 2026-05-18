package pcmuaudio

import (
	"github.com/go-gst/go-gst/gst"
)

func Register() bool {
	return gst.RegisterElement(
		nil,
		"pcmu-audio",
		gst.RankNone,
		&PcmuAudio{},
		gst.ExtendsBin,
	)
}
