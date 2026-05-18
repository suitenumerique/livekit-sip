package videovp9

import (
	"github.com/go-gst/go-gst/gst"
)

func Register() bool {
	return gst.RegisterElement(
		nil,
		"video-vp9",
		gst.RankNone,
		&VideoVp9{},
		gst.ExtendsBin,
	)
}
