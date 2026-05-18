package sipbin

import (
	"github.com/go-gst/go-gst/gst"
)

func Register() bool {
	return gst.RegisterElement(
		nil,
		"sipbin",
		gst.RankNone,
		&SipBin{},
		gst.ExtendsBin,
	)
}
