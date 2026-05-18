package sipcompositor

import (
	"github.com/go-gst/go-gst/gst"
)

func Register() bool {
	return gst.RegisterElement(
		nil,
		"sip_compositor",
		gst.RankNone,
		&SipCompositor{},
		gst.ExtendsBin,
	)
}
