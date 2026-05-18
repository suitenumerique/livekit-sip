package livekitcompositor

import (
	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/livekitcompositor/patchbay"
)

func Register() bool {
	return gst.RegisterElement(
		nil,
		"livekit_compositor",
		gst.RankNone,
		&LivekitCompositor{},
		gst.ExtendsBin,
	) && gst.RegisterElement(
		nil,
		"livekit_compositor_patchbay",
		gst.RankNone,
		&patchbay.Patchbay{},
		gst.ExtendsBin,
	)
}
