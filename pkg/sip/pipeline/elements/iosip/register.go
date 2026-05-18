package iosip

import (
	"github.com/go-gst/go-gst/gst"
)

func Register() bool {
	return gst.RegisterElement(
		nil,
		"iosip",
		gst.RankNone,
		&IoManagerSip{},
		gst.ExtendsBin,
	)
}
