package flacsource

import (
	"github.com/go-gst/go-gst/gst"
)

func Register() bool {
	return gst.RegisterElement(
		nil,
		"flacsource",
		gst.RankNone,
		&FlacSource{},
		gst.ExtendsBin,
	)
}
