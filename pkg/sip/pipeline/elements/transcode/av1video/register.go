package av1video

import (
	"github.com/go-gst/go-gst/gst"
)

func Register() bool {
	return gst.RegisterElement(
		nil,
		"av1-video",
		gst.RankNone,
		&Av1Video{},
		gst.ExtendsBin,
	)
}
