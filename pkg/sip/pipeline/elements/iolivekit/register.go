package iolivekit

import (
	"github.com/go-gst/go-gst/gst"
)

func Register() bool {
	return gst.RegisterElement(
		nil,
		"iolivekit",
		gst.RankNone,
		&IoManagerLivekit{},
		gst.ExtendsBin,
	)
}
