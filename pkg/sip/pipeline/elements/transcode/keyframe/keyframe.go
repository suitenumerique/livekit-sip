package keyframe

import (
	"time"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/video"
)

// LogFirstDecodedFrame logs once when the first buffer leaves the given
// decoder pad, then removes its probe.
func LogFirstDecodedFrame(cat *gst.DebugCategory, self *gst.Bin, pad *gst.Pad) {
	wself := glib.WeakRefInit(self)
	pad.AddProbe(gst.PadProbeTypeBuffer, func(p *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		if self := gst.ToGstBin(wself.Get()); self != nil {
			self.Log(cat, gst.LevelInfo, "First decoded frame")
		}
		return gst.PadProbeRemove
	})
}

func RequestOnBadBuffer(pad *gst.Pad) {
	var lastRequest time.Time
	lastPts := gst.ClockTimeNone

	pad.AddProbe(gst.PadProbeTypeBuffer, func(p *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		buf := info.GetBuffer()
		if buf == nil {
			return gst.PadProbeOK
		}

		bad := buf.HasFlags(gst.BufferFlagDiscont) || buf.HasFlags(gst.BufferFlagCorrupted)
		pts := buf.PresentationTimestamp()
		if pts != gst.ClockTimeNone && lastPts != gst.ClockTimeNone && pts <= lastPts {
			bad = true
		}
		if pts != gst.ClockTimeNone {
			lastPts = pts
		}
		if !bad {
			return gst.PadProbeOK
		}

		now := time.Now()
		if !lastRequest.IsZero() && now.Sub(lastRequest) < 5*time.Second {
			return gst.PadProbeOK
		}
		lastRequest = now

		p.SendEvent(video.NewEventUpstreamForceKeyUnit(gst.ClockTimeNone, true, 0))
		return gst.PadProbeOK
	})
}
