package trackfallback

import (
	"github.com/go-gst/go-gst/gst"
)

func Register() bool {
	return gst.RegisterElement(
		nil,
		"trackfallback",
		gst.RankNone,
		&TrackFallback{},
		gst.ExtendsBin,
	)
}
