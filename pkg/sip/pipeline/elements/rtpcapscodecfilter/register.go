package rtpcapscodecfilter

import (
	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/base"
)

func Register() bool {
	return gst.RegisterElement(
		nil,
		"rtpcapscodecfilter",
		gst.RankNone,
		&RtpCapsCodecFilter{},
		base.ExtendsBaseTransform,
	)
}
