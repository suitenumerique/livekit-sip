package factorybin

import (
	"github.com/go-gst/go-gst/gst"
)

func Register() bool {
	return gst.RegisterElement(
		nil,
		"factorybin",
		gst.RankNone,
		&FactoryBin{},
		gst.ExtendsBin,
	)
}
