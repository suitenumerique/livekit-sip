package vp9video

import (
	"github.com/go-gst/go-gst/gst"
)

func Register() bool {
	return gst.RegisterElement(
		nil,
		"vp9-video",
		gst.RankNone,
		&Vp9Video{},
		gst.ExtendsBin,
	)
}
