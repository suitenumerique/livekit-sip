package bfcpserver

import (
	"github.com/go-gst/go-gst/gst"
)

func Register() bool {
	return gst.RegisterElement(
		nil,
		"bfcpserver",
		gst.RankNone,
		&BFCPServer{},
		gst.ExtendsElement,
	)
}
