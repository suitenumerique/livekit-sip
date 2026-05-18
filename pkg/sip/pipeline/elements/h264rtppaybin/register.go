package h264rtppaybin

import (
	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/base"
)

// Register installs both the public h264rtppaybin bin and its internal
// h264rtpplidpatch passthrough with the GStreamer registry. The plid
// patcher is only intended to be used by H264RtpPayBin.
func Register() bool {
	if !gst.RegisterElement(
		nil,
		"h264rtpplidpatch",
		gst.RankNone,
		&plidPatch{},
		base.ExtendsBaseTransform,
	) {
		return false
	}
	return gst.RegisterElement(
		nil,
		"h264rtppaybin",
		gst.RankNone,
		&H264RtpPayBin{},
		gst.ExtendsBin,
	)
}
