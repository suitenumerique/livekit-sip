package sipbin

import (
	"errors"
	"fmt"
	"math"

	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/livekit"
)

func (e *SipBin) OnAckSDP(self *gst.Bin, b []byte) error {
	unlock, err := e.transaction.Ack(TransactionPendingKindAck)
	if err != nil {
		// A stray/unexpected ACK (e.g. the ACK of a glare-rejected re-INVITE, or one
		// arriving while a re-INVITE renegotiation is pending) must not be fatal:
		// returning an error escalates to self.Error() and tears down the whole
		// pipeline. Log and ignore it instead.
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Ignoring unexpected ACK (no matching pending transaction)\nerr=%v", err))
		return nil
	}
	defer unlock()

	e.mu.Lock()
	defer e.mu.Unlock()

	e.transactionID.Add(1)

	// late offer answer
	if len(b) > 0 {
		if err := e.handleAnswerSdp(self, b); err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to handle answer SDP in AckSDP\nerr=%v", err))
			return err
		}
	}

	return nil
}

func (e *SipBin) ToggleScreenshare(self *gst.Bin, enable bool) {
	if enable {
		e.bfcpStartScreenshare(self)
	} else {
		e.bfcpStopScreenshare(self)
	}
}

func (e *SipBin) emitAvailableMedia(self *gst.Bin) {
	camera := e.Tracks[livekit.TrackSource_CAMERA] != nil && e.Tracks[livekit.TrackSource_CAMERA].send
	microphone := e.Tracks[livekit.TrackSource_MICROPHONE] != nil && e.Tracks[livekit.TrackSource_MICROPHONE].send
	screenShare := e.Tracks[livekit.TrackSource_SCREEN_SHARE] != nil && e.Tracks[livekit.TrackSource_SCREEN_SHARE].send
	screenShareAudio := e.Tracks[livekit.TrackSource_SCREEN_SHARE_AUDIO] != nil && e.Tracks[livekit.TrackSource_SCREEN_SHARE_AUDIO].send

	e.mu.Unlock()
	defer e.mu.Lock()
	if _, err := self.Emit("available-media", camera, microphone, screenShare, screenShareAudio); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to emit available-media signal\nerr=%v", err))
	}
}

func (e *SipBin) onRtpBinRequestPtMap(self *gst.Bin, session int, pt uint8) *gst.Caps {
	e.mu.Lock()
	defer e.mu.Unlock()

	kind := livekit.TrackSource(session)

	switch kind {
	case livekit.TrackSource_CAMERA, livekit.TrackSource_SCREEN_SHARE,
		livekit.TrackSource_MICROPHONE, livekit.TrackSource_SCREEN_SHARE_AUDIO:
	default:
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Received request for payload type map for unsupported track source\nsource=%d", kind))
		return nil
	}

	caps, exist := e.PtMap[kind][pt]
	if !exist {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Received request for payload type map for payload type which was not in the original offer for track source\npt=%d\nsource=%d", pt, kind))
		return nil
	}

	return caps
}

func (e *SipBin) onRtpBinSenderTimeout(self *gst.Bin, session, ssrc uint) {
	kind := livekit.TrackSource(session)
	switch kind {
	case livekit.TrackSource_CAMERA, livekit.TrackSource_SCREEN_SHARE,
		livekit.TrackSource_MICROPHONE, livekit.TrackSource_SCREEN_SHARE_AUDIO:
	default:
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Received sender timeout for unsupported track source\nsource=%d", kind))
		return
	}

	self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Sender timeout\nsource=%d\nssrc=%d", kind, ssrc))

	if _, err := e.RtpBin.Emit("clear-ssrc", session, ssrc); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to emit clear-ssrc signal on rtpbin\nsession=%d\nssrc=%d\nerr=%v", session, ssrc, err))
	}
}

func (e *SipBin) onRtpBinSsrcCollision(self *gst.Bin, session, ssrc uint) {
	kind := livekit.TrackSource(session)
	switch kind {
	case livekit.TrackSource_CAMERA, livekit.TrackSource_SCREEN_SHARE,
		livekit.TrackSource_MICROPHONE, livekit.TrackSource_SCREEN_SHARE_AUDIO:
	default:
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Received SSRC collision for unsupported track source\nsource=%d", kind))
		return
	}

	self.Log(CAT, gst.LevelWarning, fmt.Sprintf("SSRC collision detected\nsource=%d\nssrc=%d", kind, ssrc))

	if _, err := e.RtpBin.Emit("clear-ssrc", session, ssrc); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to emit clear-ssrc signal on rtpbin\nsession=%d\nssrc=%d\nerr=%v", session, ssrc, err))
	}
}

func (e *SipBin) onRtpBinNewJitterbuffer(self *gst.Bin, jitterbuffer *gst.Element, session, ssrc uint) {
	return
	kind := livekit.TrackSource(session)
	switch kind {
	case livekit.TrackSource_CAMERA, livekit.TrackSource_SCREEN_SHARE,
		livekit.TrackSource_MICROPHONE, livekit.TrackSource_SCREEN_SHARE_AUDIO:
	default:
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Received new jitterbuffer for unsupported track source\nsource=%d", kind))
		return
	}

	if err := errors.Join(
		jitterbuffer.SetProperty("mode", int(0)),
		jitterbuffer.SetProperty("max-dropout-time", uint(math.MaxInt32)),
	); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to set properties on new jitterbuffer\nsource=%d\nsession=%d\nssrc=%d\nerr=%v", kind, session, ssrc, err))
		self.Error(fmt.Sprintf("Failed to set properties on new jitterbuffer for track source %d, session %d, and ssrc %d", kind, session, ssrc), err)
	}
}

func (e *SipBin) onRtpBinPadAdded(self *gst.Bin, pad *gst.Pad) {
	templ := pad.Template()
	if templ == nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Received new pad on rtpbin, but failed to get template\npad=%s", pad.GetName()))
		return
	}

	switch templ.GetName() {
	case "send_rtp_src_%u":
		e.onRtpBinPadAddedSendRtpSrc(self, pad)
	case "recv_rtp_src_%u_%u_%u":
		e.onRtpBinPadAddedRecvRtpSrc(self, pad)
	}
}

func (e *SipBin) onRtpBinPadAddedSendRtpSrc(self *gst.Bin, pad *gst.Pad) {
	var session int
	if _, err := fmt.Sscanf(pad.GetName(), "send_rtp_src_%d", &session); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Received new pad on rtpbin, but failed to parse session number\npad=%s\nerr=%v", pad.GetName(), err))
		return
	}

	kind := livekit.TrackSource(session)
	switch kind {
	case livekit.TrackSource_CAMERA, livekit.TrackSource_SCREEN_SHARE,
		livekit.TrackSource_MICROPHONE, livekit.TrackSource_SCREEN_SHARE_AUDIO:
	default:
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Received new pad for unsupported track source\npad=%s\nsource=%d", pad.GetName(), kind))
		return
	}

	// WARNING: this is callind in sync after the rtpbin pad request which already hold the lock. locking here cause deadlock because we are already locked
	// e.mu.Lock()
	// defer e.mu.Unlock()

	ti := e.Tracks[kind]
	if ti == nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Received new pad for track source, but no track info found\npad=%s\nsource=%d", pad.GetName(), kind))
		return
	}

	if ret := pad.Link(ti.RtpSink.GetStaticPad("sink")); ret != gst.PadLinkOK {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link new pad to RTP sink for track source\npad=%s\nsource=%d\nerr=%v", pad.GetName(), kind, ret))
		self.Error(fmt.Sprintf("Failed to link new pad %s to RTP sink for track source %d", pad.GetName(), kind), fmt.Errorf("link failed: %v", ret))
		return
	}

	self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Linked new pad from rtpbin to RTP sink for track source\npad=%s\nsource=%d", pad.GetName(), kind))
}

func (e *SipBin) onRtpBinPadAddedRecvRtpSrc(self *gst.Bin, pad *gst.Pad) {
	var (
		session int
		ssrc    int
		pt      int
	)

	if _, err := fmt.Sscanf(pad.GetName(), "recv_rtp_src_%d_%d_%d", &session, &ssrc, &pt); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Received new pad on rtpbin, but failed to parse session, ssrc, and payload type\npad=%s\nerr=%v", pad.GetName(), err))
		return
	}

	kind := livekit.TrackSource(session)
	switch kind {
	case livekit.TrackSource_CAMERA, livekit.TrackSource_SCREEN_SHARE,
		livekit.TrackSource_MICROPHONE, livekit.TrackSource_SCREEN_SHARE_AUDIO:
	default:
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Received new pad for unsupported track source\npad=%s\nsource=%d", pad.GetName(), kind))
		return
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	if _, exist := e.PtMap[kind][uint8(pt)]; !exist {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Received new pad for payload type which was not in the original offer for track source\npad=%s\npt=%d\nsource=%d", pad.GetName(), pt, kind))
		return
	}

	class := gst.ToElementClass(self.Class())

	gpad := gst.NewGhostPadFromTemplate(pad.GetName(), pad, class.GetPadTemplate("recv_rtp_src_%u_%u_%u"))
	if gpad == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create ghost pad for new RTP source pad\npad=%s", pad.GetName()))
		return
	}
	if !self.AddPad(gpad.Pad) {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add ghost pad for new RTP source pad\npad=%s", pad.GetName()))
		return
	}
	if !gpad.SetActive(true) {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to activate ghost pad for new RTP source pad\npad=%s", pad.GetName()))
		return
	}
}

func (e *SipBin) onRtpBinPadRemoved(self *gst.Bin, pad *gst.Pad) {
	templ := pad.Template()
	if templ == nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Pad removed from rtpbin, but failed to get template\npad=%s", pad.GetName()))
		return
	}

	switch templ.GetName() {
	case "send_rtp_src_%u":
		e.onRtpBinPadRemovedSendRtpSrc(self, pad)
	case "recv_rtp_src_%u_%u_%u":
		e.onRtpBinPadRemovedRecvRtpSrc(self, pad)
	}
}

func (e *SipBin) onRtpBinPadRemovedRecvRtpSrc(self *gst.Bin, pad *gst.Pad) {
	var session, ssrc, pt int
	if _, err := fmt.Sscanf(pad.GetName(), "recv_rtp_src_%d_%d_%d", &session, &ssrc, &pt); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Pad removed from rtpbin, but failed to parse session, ssrc, and payload type\npad=%s\nerr=%v", pad.GetName(), err))
		return
	}

	kind := livekit.TrackSource(session)
	switch kind {
	case livekit.TrackSource_CAMERA, livekit.TrackSource_SCREEN_SHARE,
		livekit.TrackSource_MICROPHONE, livekit.TrackSource_SCREEN_SHARE_AUDIO:
	default:
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Pad removed from rtpbin for unsupported track source\npad=%s\nsource=%d", pad.GetName(), kind))
		return
	}

	self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Pad removed from rtpbin for track source\npad=%s\nsource=%d\nssrc=%d\npt=%d", pad.GetName(), kind, ssrc, pt))

	gpad := self.GetStaticPad(fmt.Sprintf("recv_rtp_src_%d_%d_%d", session, ssrc, pt))
	if gpad == nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to get ghost pad for removed RTP source pad\npad=%s", pad.GetName()))
		return
	}

	if !self.RemovePad(gpad) {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to remove ghost pad for removed RTP source pad\npad=%s", pad.GetName()))
		return
	}
}

func (e *SipBin) onRtpBinPadRemovedSendRtpSrc(self *gst.Bin, pad *gst.Pad) {
	var session int
	if _, err := fmt.Sscanf(pad.GetName(), "send_rtp_src_%d", &session); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Pad removed from rtpbin, but failed to parse session number\npad=%s\nerr=%v", pad.GetName(), err))
		return
	}

	kind := livekit.TrackSource(session)
	switch kind {
	case livekit.TrackSource_CAMERA, livekit.TrackSource_SCREEN_SHARE,
		livekit.TrackSource_MICROPHONE, livekit.TrackSource_SCREEN_SHARE_AUDIO:
	default:
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Pad removed from rtpbin for unsupported track source\npad=%s\nsource=%d", pad.GetName(), kind))
		return
	}

	ti := e.Tracks[kind]
	if ti == nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Pad removed from rtpbin for track source, but no track info found\npad=%s\nsource=%d", pad.GetName(), kind))
		return
	}

	self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Pad removed from rtpbin, cleaned up track for track source\npad=%s\nsource=%d", pad.GetName(), kind))
}
