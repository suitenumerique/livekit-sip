package videoav1

import (
	"github.com/go-gst/go-gst/gst"
)

func Register() bool {
	return gst.RegisterElement(
		nil,
		"video-av1",
		gst.RankNone,
		&VideoAv1{},
		gst.ExtendsBin,
	)
}
