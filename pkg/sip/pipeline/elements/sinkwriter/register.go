package sinkwriter

import (
	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/base"
)

func Register() bool {
	return gst.RegisterElement(
		// no plugin:
		nil,
		// The name of the element
		"sinkwriter",
		// The rank of the element
		gst.RankNone,
		// The GoElement implementation for the element
		&sinkWriter{},
		// The base subclass this element extends
		base.ExtendsBaseSink,
	)
}
