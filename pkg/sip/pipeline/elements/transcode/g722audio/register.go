package g722audio

import (
	"github.com/go-gst/go-gst/gst"
)

func Register() bool {
	return gst.RegisterElement(
		nil,
		"g722-audio",
		gst.RankNone,
		&G722Audio{},
		gst.ExtendsBin,
	)
}
