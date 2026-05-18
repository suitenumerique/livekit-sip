package audioopus

import (
	"github.com/go-gst/go-gst/gst"
)

func Register() bool {
	return gst.RegisterElement(
		nil,
		"audio-opus",
		gst.RankNone,
		&AudioOpus{},
		gst.ExtendsBin,
	)
}
