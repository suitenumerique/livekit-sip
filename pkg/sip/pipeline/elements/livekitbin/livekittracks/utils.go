package livekittracks

import "github.com/go-gst/go-gst/gst"

var CAT *gst.DebugCategory

func PadProbeDrop(_ *gst.Pad, _ *gst.PadProbeInfo) gst.PadProbeReturn {
	return gst.PadProbeDrop
}

func PadProbeDropEOS(_ *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
	if info.GetEvent() != nil && info.GetEvent().Type() == gst.EventTypeEOS {
		return gst.PadProbeDrop
	}
	return gst.PadProbeOK
}
