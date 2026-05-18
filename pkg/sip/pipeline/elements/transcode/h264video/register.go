package h264video

import (
	"github.com/go-gst/go-gst/gst"
)

func Register() bool {
	return gst.RegisterElement(
		nil,
		"h264-video",
		gst.RankNone,
		&H264Video{},
		gst.ExtendsBin,
	)
}
