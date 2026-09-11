package keyframe

import (
	"fmt"
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

// LogResolutionChanges logs every change of the decoded frame size after the
// first caps seen on pad.
func LogResolutionChanges(cat *gst.DebugCategory, self *gst.Bin, pad *gst.Pad) {
	wself := glib.WeakRefInit(self)
	var lastWidth, lastHeight int
	pad.AddProbe(gst.PadProbeTypeEventDownstream, func(_ *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		ev := info.GetEvent()
		if ev == nil || ev.Type() != gst.EventTypeCaps {
			return gst.PadProbeOK
		}
		caps := ev.ParseCaps()
		if caps == nil || caps.GetSize() == 0 {
			return gst.PadProbeOK
		}
		st := caps.GetStructureAt(0)
		w, errW := st.GetValue("width")
		h, errH := st.GetValue("height")
		if errW != nil || errH != nil {
			return gst.PadProbeOK
		}
		width, okW := w.(int)
		height, okH := h.(int)
		if !okW || !okH {
			return gst.PadProbeOK
		}
		if lastWidth != 0 && (width != lastWidth || height != lastHeight) {
			if self := gst.ToGstBin(wself.Get()); self != nil {
				self.Log(cat, gst.LevelInfo, fmt.Sprintf("Decoded resolution changed\nfrom=%dx%d\nto=%dx%d", lastWidth, lastHeight, width, height))
			}
		}
		lastWidth, lastHeight = width, height
		return gst.PadProbeOK
	})
}
