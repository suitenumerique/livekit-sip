package audiog722

import (
	"github.com/go-gst/go-gst/gst"
)

func Register() bool {
	return gst.RegisterElement(
		nil,
		"audio-g722",
		gst.RankNone,
		&AudioG722{},
		gst.ExtendsBin,
	)
}
