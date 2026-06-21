package livekitbin

import (
	"fmt"
	"weak"

	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/livekit"
)

func (e *LivekitBin) setupRtpBinSignals(self *gst.Bin) {
	eweak := weak.Make(e)
	if _, err := e.RtpBin.Connect("pad-added", func(_ *gst.Element, pad *gst.Pad) {
		ptr := eweak.Value()
		if ptr == nil {
			return
		}
		ptr.OnRtpBinPadAdded(pad)
	}); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error connecting to rtpbin pad-added signal\nerr=%v", err))
		self.Error("Error connecting to rtpbin pad-added signal", err)
		return
	}

	if _, err := e.RtpBin.Connect("pad-removed", func(_ *gst.Element, pad *gst.Pad) {
		ptr := eweak.Value()
		if ptr == nil {
			return
		}
		ptr.OnRtpBinPadRemoved(pad)
	}); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error connecting to rtpbin pad-removed signal\nerr=%v", err))
		self.Error("Error connecting to rtpbin pad-removed signal", err)
		return
	}

	if _, err := e.RtpBin.Connect("request-pt-map", func(_ *gst.Element, session, pt uint) *gst.Caps {
		ptr := eweak.Value()
		if ptr == nil {
			return nil
		}
		return ptr.OnRtpBinRequestPtMap(session, pt)
	}); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error connecting to rtpbin request-pt-map signal\nerr=%v", err))
		self.Error("Error connecting to rtpbin request-pt-map signal", err)
		return
	}

	if _, err := e.RtpBin.Connect("on-ssrc-collision", func(_ *gst.Element, session uint, ssrc uint) {
		ptr := eweak.Value()
		if ptr == nil {
			return
		}
		ptr.OnSSRCCollision(session, ssrc)
	}); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error connecting to rtpbin on-ssrc-collision signal\nerr=%v", err))
		self.Error("Error connecting to rtpbin on-ssrc-collision signal", err)
		return
	}

	if _, err := e.RtpBin.Connect("on-timeout", func(_ *gst.Element, session uint, ssrc uint) {
		ptr := eweak.Value()
		if ptr == nil {
			return
		}
		ptr.OnTimeout(session, ssrc)
	}); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error connecting to rtpbin on-timeout signal\nerr=%v", err))
		self.Error("Error connecting to rtpbin on-timeout signal", err)
		return
	}
}

func (e *LivekitBin) OnRtpBinPadAdded(pad *gst.Pad) {
	self := gst.ToGstBin(e.self.Get())
	if self == nil || self.Instance() == nil {
		return
	}

	pname := pad.GetName()
	templ := pad.Template()
	if templ == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("No pad template found for pad\npad=%s", pname))
		return
	}

	handles := []struct {
		template string
		handler  func(self *gst.Bin, pad *gst.Pad, pname string)
	}{
		{"send_rtp_src_%u", e.onRtpBinPadAddedSendRtp},
		{"recv_rtp_src_%u_%u_%u", e.onRtpBinPadAddedRecvRtp},
	}

	for _, h := range handles {
		if templ.GetName() == h.template {
			h.handler(self, pad, pname)
			return
		}
	}

	self.Log(CAT, gst.LevelLog, fmt.Sprintf("No handler for pad\npad=%s\ntemplate=%s", pname, templ.GetName()))
}

func (e *LivekitBin) OnRtpBinPadRemoved(pad *gst.Pad) {
	self := gst.ToGstBin(e.self.Get())
	if self == nil || self.Instance() == nil {
		return
	}

	pname := pad.GetName()
	templ := pad.Template()
	if templ == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("No pad template found for pad\npad=%s", pname))
		return
	}

	handles := []struct {
		template string
		handler  func(self *gst.Bin, pad *gst.Pad, pname string)
	}{
		{"send_rtp_src_%u", e.onRtpBinPadRemovedSendRtp},
		{"recv_rtp_src_%u_%u_%u", e.onRtpBinPadRemovedRecvRtp},
	}

	for _, h := range handles {
		if templ.GetName() == h.template {
			h.handler(self, pad, pname)
			return
		}
	}

	self.Log(CAT, gst.LevelLog, fmt.Sprintf("No handler for pad\npad=%s\ntemplate=%s", pname, templ.GetName()))
}

func (e *LivekitBin) OnRtpBinRequestPtMap(session, pt uint) *gst.Caps {
	self := gst.ToGstBin(e.self.Get())
	if self == nil || self.Instance() == nil {
		return nil
	}

	kind := livekit.TrackSource(session)

	switch kind {
	case livekit.TrackSource_CAMERA, livekit.TrackSource_MICROPHONE, livekit.TrackSource_SCREEN_SHARE, livekit.TrackSource_SCREEN_SHARE_AUDIO:
	default:
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Unknown track source in rtpbin request-pt-map callback\nsession=%d", session))
		return nil
	}

	e.ptMu.RLock()
	defer e.ptMu.RUnlock()
	caps, ok := e.PtMap[kind][uint8(pt)]

	if !ok {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Unknown payload type in rtpbin request-pt-map callback\npt=%d", pt))
		return nil
	}

	return caps
}

func (e *LivekitBin) OnSSRCCollision(session, ssrc uint) {
	self := gst.ToGstBin(e.self.Get())
	if self == nil || self.Instance() == nil {
		return
	}

	self.Log(CAT, gst.LevelWarning, fmt.Sprintf("SSRC collision detected\nsession=%d\nssrc=%d", session, ssrc))
}

func (e *LivekitBin) OnTimeout(session, ssrc uint) {
	self := gst.ToGstBin(e.self.Get())
	if self == nil || self.Instance() == nil {
		return
	}

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("SSRC has timed out\nssrc=%d\nsession=%d", ssrc, session))

	if _, err := e.RtpBin.Emit("clear-ssrc", session, ssrc); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error emitting clear-ssrc signal\nerr=%v", err))
		self.Error("Error emitting clear-ssrc signal", err)
		return
	}
}
