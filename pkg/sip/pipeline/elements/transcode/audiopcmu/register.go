package audiopcmu

import (
	"github.com/go-gst/go-gst/gst"
)

func Register() bool {
	return gst.RegisterElement(
		nil,
		"audio-pcmu",
		gst.RankNone,
		&AudioPcmu{},
		gst.ExtendsBin,
	)
}
