package vp8video

import (
	"github.com/go-gst/go-gst/gst"
)

func Register() bool {
	return gst.RegisterElement(
		nil,
		"vp8-video",
		gst.RankNone,
		&Vp8Video{},
		gst.ExtendsBin,
	)
}
