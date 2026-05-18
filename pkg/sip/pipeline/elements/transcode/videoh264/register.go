package videoh264

import (
	"github.com/go-gst/go-gst/gst"
)

func Register() bool {
	return gst.RegisterElement(
		nil,
		"video-h264",
		gst.RankNone,
		&VideoH264{},
		gst.ExtendsBin,
	)
}
