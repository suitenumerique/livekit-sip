package audiopcma

import (
	"github.com/go-gst/go-gst/gst"
)

func Register() bool {
	return gst.RegisterElement(
		nil,
		"audio-pcma",
		gst.RankNone,
		&AudioPcma{},
		gst.ExtendsBin,
	)
}
