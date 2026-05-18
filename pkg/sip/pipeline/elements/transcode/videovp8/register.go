package videovp8

import (
	"github.com/go-gst/go-gst/gst"
)

func Register() bool {
	return gst.RegisterElement(
		nil,
		"video-vp8",
		gst.RankNone,
		&VideoVp8{},
		gst.ExtendsBin,
	)
}
