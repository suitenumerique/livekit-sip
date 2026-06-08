package livekitcompositor

import (
	"github.com/go-gst/go-gst/gst"
)

func Register() bool {
	return gst.RegisterElement(
		nil,
		"livekit_compositor",
		gst.RankNone,
		&LivekitCompositor{},
		gst.ExtendsBin,
	)
}
