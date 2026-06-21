package sipbin

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"
	"weak"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/gstsdp"
	"github.com/livekit/protocol/livekit"
)

var CAT = gst.NewDebugCategory(
	"sipbin",
	gst.DebugColorFgBlue,
	"sipbin Element",
)

const NbTracks = int(livekit.TrackSource_SCREEN_SHARE_AUDIO) + 1

type EncodingCase int

const (
	EncodingCaseLower EncodingCase = iota
	EncodingCaseUpper
)

type SipBin struct {
	config
	mu sync.Mutex

	RtpBin *gst.Element

	encodingCase [NbTracks]map[uint8]EncodingCase // indexed by livekit.TrackSource
	PtMap        [NbTracks]map[uint8]*gst.Caps    // indexed by livekit.TrackSource
	Tracks       [NbTracks]*SipTrack              // indexed by livekit.TrackSource
	Medias       []*gstsdp.Media

	Bfcp *BfcpTrack

	transaction   *SipTransaction
	transactionID atomic.Uint64
	pendingOffer  *pendingOffer
	sdpVersion    uint64 // o= line version, incremented per generated SDP (RFC 3264 §8)

	wg sync.WaitGroup
}

var (
	SignalOfferSdpID          uint
	SignalAnswerSdpID         uint
	SignalAckSdpID            uint
	SignalAbortOfferID        uint
	SignalCreateOfferSdpID    uint
	SignalToggleScreenshareID uint
	SignalStatsID             uint
	SignalSendOfferSdpID      uint
	SignalAvailableMediaID    uint
)

func (e *SipBin) New() glib.GoObjectSubclass {
	return &SipBin{}
}

func (e *SipBin) ClassInit(klass *glib.ObjectClass) {
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"SipBin",
		"Generic",
		"SipBin Element",
		"Roomkit <roomkit-visio@numerique.gouv.fr>",
	)

	// action signals
	SignalOfferSdpID = gst.SignalNew(
		class.Type(),
		"offer-sdp",
		gst.SignalRunLast,
		glib.TYPE_STRING,
		glib.TYPE_STRING,
	)

	SignalAnswerSdpID = gst.SignalNew(
		class.Type(),
		"answer-sdp",
		gst.SignalRunLast,
		glib.TYPE_STRING,
		glib.TYPE_STRING,
	)

	SignalAckSdpID = gst.SignalNew(
		class.Type(),
		"ack-sdp",
		gst.SignalRunLast,
		glib.TYPE_NONE,
		glib.TYPE_STRING,
	)

	SignalAbortOfferID = gst.SignalNew(
		class.Type(),
		"abort-offer",
		gst.SignalRunLast,
		glib.TYPE_NONE,
	)

	SignalCreateOfferSdpID = gst.SignalNew(
		class.Type(),
		"create-offer-sdp",
		gst.SignalRunLast,
		glib.TYPE_STRING,
	)

	SignalToggleScreenshareID = gst.SignalNew(
		class.Type(),
		"toggle-screenshare",
		gst.SignalRunLast,
		glib.TYPE_NONE,
		glib.TYPE_BOOLEAN,
	)

	SignalStatsID = gst.SignalNew(
		class.Type(),
		"stats",
		gst.SignalRunLast,
		gst.TypeStructure,
	)

	// request signals
	SignalSendOfferSdpID = gst.SignalNew(
		class.Type(),
		"send-offer-sdp",
		gst.SignalRunLast,
		glib.TYPE_NONE,
		glib.TYPE_STRING,
	)

	SignalAvailableMediaID = gst.SignalNew(
		class.Type(),
		"available-media",
		gst.SignalRunLast,
		glib.TYPE_NONE,
		glib.TYPE_BOOLEAN, // camera
		glib.TYPE_BOOLEAN, // microphone
		glib.TYPE_BOOLEAN, // screen share
		glib.TYPE_BOOLEAN, // screen share audio (never used)
	)

	class.AddPadTemplate(gst.NewPadTemplate(
		"recv_rtp_src_%u_%u_%u",
		gst.PadDirectionSource,
		gst.PadPresenceSometimes,
		gst.NewCapsFromString("application/x-rtp"),
	))

	class.AddPadTemplate(gst.NewPadTemplate(
		"send_rtp_sink_%u",
		gst.PadDirectionSink,
		gst.PadPresenceRequest,
		gst.NewCapsFromString("application/x-rtp"),
	))

	class.InstallProperties(properties)
}

func (e *SipBin) InstanceInit(instance *glib.Object) {
	self := gst.ToGstBin(instance)

	e.mu.Lock()
	defer e.mu.Unlock()

	e.transaction = NewSipTransaction()

	for i := range e.PtMap {
		e.PtMap[i] = make(map[uint8]*gst.Caps)
	}

	for i := range e.encodingCase {
		e.encodingCase[i] = make(map[uint8]EncodingCase)
	}

	e.sessionID = randID()

	eweak := weak.Make(e)
	wself := glib.WeakRefInit(self)
	if _, err := self.Connect("offer-sdp", func(instance *gst.Element, offer string) string {
		e := eweak.Value()
		if e == nil {
			return ""
		}
		self := gst.ToGstBin(instance)
		answerData, err := e.OnOfferSdp(self, []byte(offer))
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("failed to process offer\nerr=%v", err))
			self.Error("failed to process offer", err)
			return ""
		}
		return string(answerData)
	}); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("failed to connect offer-sdp signal\nerr=%v", err))
		self.Error("failed to connect offer-sdp signal", err)
		return
	}

	if _, err := self.Connect("answer-sdp", func(instance *gst.Element, answer string) {
		e := eweak.Value()
		if e == nil {
			return
		}
		self := gst.ToGstBin(instance)
		err := e.OnAnswerSdp(self, []byte(answer))
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("failed to process answer\nerr=%v", err))
			self.Error("failed to process answer", err)
			return
		}
	}); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("failed to connect answer-sdp signal\nerr=%v", err))
		self.Error("failed to connect answer-sdp signal", err)
		return
	}

	if _, err := self.Connect("ack-sdp", func(instance *gst.Element, ack string) {
		e := eweak.Value()
		if e == nil {
			return
		}
		self := gst.ToGstBin(instance)
		err := e.OnAckSDP(self, []byte(ack))
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("failed to process ack\nerr=%v", err))
			self.Error("failed to process ack", err)
			return
		}
	}); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("failed to connect ack-sdp signal\nerr=%v", err))
		self.Error("failed to connect ack-sdp signal", err)
		return
	}

	if _, err := self.Connect("abort-offer", func(instance *gst.Element) {
		e := eweak.Value()
		if e == nil {
			return
		}
		self := gst.ToGstBin(instance)
		e.OnOfferAborted(self)
	}); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("failed to connect abort-offer signal: %v", err))
		self.Error("failed to connect abort-offer signal", err)
		return
	}

	if _, err := self.Connect("create-offer-sdp", func(instance *gst.Element) string {
		e := eweak.Value()
		if e == nil {
			return ""
		}
		self := gst.ToGstBin(instance)
		offerData, err := e.OnCreateOfferSDP(self)
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("failed to create offer\nerr=%v", err))
			self.Error("failed to create offer", err)
			return ""
		}
		return string(offerData)
	}); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("failed to connect create-offer-sdp signal\nerr=%v", err))
		self.Error("failed to connect create-offer-sdp signal", err)
		return
	}

	if _, err := self.Connect("toggle-screenshare", func(instance *gst.Element, enable bool) {
		e := eweak.Value()
		if e == nil {
			return
		}
		self := gst.ToGstBin(instance)
		e.ToggleScreenshare(self, enable)
	}); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("failed to connect toggle-screenshare signal\nerr=%v", err))
		self.Error("failed to connect toggle-screenshare signal", err)
		return
	}

	if _, err := self.Connect("stats", func(instance *gst.Element) *gst.Structure {
		e := eweak.Value()
		if e == nil {
			return nil
		}
		self := gst.ToGstBin(instance)
		return e.DumpStats(self)
	}); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("failed to connect stats signal\nerr=%v", err))
		self.Error("failed to connect stats signal", err)
		return
	}

	var err error
	e.RtpBin, err = gst.NewElementWithProperties("rtpbin", map[string]interface{}{
		"rtp-profile":              int(3), // GST_RTP_PROFILE_AVPF
		"autoremove":               true,
		"max-misorder-time":        uint(0),
		"max-dropout-time":         uint(200),
		"max-ts-offset":            int(200000000),
		"timeout-inactive-sources": true,
		"drop-on-latency":          false,
		"latency":                  uint(200),
		"do-lost":                  true,
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("failed to create rtpbin element\nerr=%v", err))
		self.Error("failed to create rtpbin element", err)
		return
	}
	if _, err := e.RtpBin.Connect("pad-added", func(_ *gst.Element, pad *gst.Pad) {
		e := eweak.Value()
		self := gst.ToGstBin(wself.Get())
		if e == nil || self == nil || self.Instance() == nil {
			return
		}
		e.onRtpBinPadAdded(self, pad)
	}); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("failed to connect pad-added signal\nerr=%v", err))
		self.Error("failed to connect pad-added signal", err)
		return
	}
	if _, err := e.RtpBin.Connect("pad-removed", func(_ *gst.Element, pad *gst.Pad) {
		e := eweak.Value()
		self := gst.ToGstBin(wself.Get())
		if e == nil || self == nil || self.Instance() == nil {
			return
		}
		e.onRtpBinPadRemoved(self, pad)
	}); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("failed to connect pad-removed signal\nerr=%v", err))
		self.Error("failed to connect pad-removed signal", err)
		return
	}
	if _, err := e.RtpBin.Connect("request-pt-map", func(_ *gst.Element, session int, pt uint8) *gst.Caps {
		e := eweak.Value()
		self := gst.ToGstBin(wself.Get())
		if e == nil || self == nil || self.Instance() == nil {
			return nil
		}
		return e.onRtpBinRequestPtMap(self, session, pt)
	}); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("failed to connect request-pt-map signal\nerr=%v", err))
		self.Error("failed to connect request-pt-map signal", err)
		return
	}
	if _, err := e.RtpBin.Connect("on-sender-timeout", func(_ *gst.Element, session, ssrc uint) {
		e := eweak.Value()
		self := gst.ToGstBin(wself.Get())
		if e == nil || self == nil || self.Instance() == nil {
			return
		}
		e.onRtpBinSenderTimeout(self, session, ssrc)
	}); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("failed to connect on-sender-timeout signal\nerr=%v", err))
		self.Error("failed to connect on-sender-timeout signal", err)
		return
	}
	if _, err := e.RtpBin.Connect("on-ssrc-collision", func(_ *gst.Element, session, ssrc uint) {
		e := eweak.Value()
		self := gst.ToGstBin(wself.Get())
		if e == nil || self == nil || self.Instance() == nil {
			return
		}
		e.onRtpBinSsrcCollision(self, session, ssrc)
	}); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("failed to connect on-ssrc-collision signal\nerr=%v", err))
		self.Error("failed to connect on-ssrc-collision signal", err)
		return
	}
	if _, err := e.RtpBin.Connect("new-jitterbuffer", func(_ *gst.Element, jitterbuffer *gst.Element, session, ssrc uint) {
		e := eweak.Value()
		self := gst.ToGstBin(wself.Get())
		if e == nil || self == nil || self.Instance() == nil {
			return
		}
		e.onRtpBinNewJitterbuffer(self, jitterbuffer, session, ssrc)
	}); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("failed to connect on-ssrc-collision signal\nerr=%v", err))
		self.Error("failed to connect on-ssrc-collision signal", err)
		return
	}

	if err := self.Add(e.RtpBin); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("failed to add rtpbin element to bin\nerr=%v", err))
		self.Error("failed to add rtpbin element to bin", err)
		return
	}
}

func (e *SipBin) ChangeState(instance *gst.Element, transition gst.StateChange) gst.StateChangeReturn {
	self := gst.ToGstBin(instance)

	ret := self.ParentChangeState(transition)
	if ret != gst.StateChangeSuccess {
		return ret
	}

	if transition == gst.StateChangeReadyToNull {
		e.transaction.Close()
		done := make(chan struct{})
		go func() {
			e.wg.Wait()
			close(done)
		}()

		select {
		case <-done:
		case <-time.After(5 * time.Second):
			self.Log(CAT, gst.LevelWarning, "Timeout waiting for SipBin to finish pending operations during state change to NULL")
		}
	}

	return ret
}

func (e *SipBin) Finalize(instance *glib.Object) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, track := range e.Tracks {
		if track != nil {
			if track.rtpConn != nil {
				track.rtpConn.Close()
			}
			if track.rtcpConn != nil {
				track.rtcpConn.Close()
			}
		}
	}
	e.Bfcp = nil
	e.Tracks = [NbTracks]*SipTrack{}
	e.PtMap = [NbTracks]map[uint8]*gst.Caps{}
	for i := range e.PtMap {
		e.PtMap[i] = make(map[uint8]*gst.Caps)
	}
	for i := range e.encodingCase {
		e.encodingCase[i] = make(map[uint8]EncodingCase)
	}
	e.RtpBin = nil
	e.Medias = nil
}

func (e *SipBin) RequestNewPad(instance *gst.Element, templ *gst.PadTemplate, name string, caps *gst.Caps) *gst.Pad {
	self := gst.ToGstBin(instance)
	if templ == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to request new pad from template: template is nil\npad=%s", name))
		return nil
	}

	switch templ.GetName() {
	case "send_rtp_sink_%u":
		return e.requestNewPadSendRtpSink(self, templ, name, caps)
	default:
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to request new pad from template: unrecognized template\npad=%s\ntemplate=%s", name, templ.GetName()))
		return nil
	}
}

func (e *SipBin) requestNewPadSendRtpSink(self *gst.Bin, templ *gst.PadTemplate, name string, caps *gst.Caps) *gst.Pad {
	var session int
	if _, err := fmt.Sscanf(name, "send_rtp_sink_%d", &session); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to parse session number for pad request\npad=%s\ntemplate=%s\nerr=%v", name, templ.GetName(), err))
		return nil
	}

	kind := livekit.TrackSource(session)
	switch kind {
	case livekit.TrackSource_CAMERA, livekit.TrackSource_SCREEN_SHARE,
		livekit.TrackSource_MICROPHONE, livekit.TrackSource_SCREEN_SHARE_AUDIO:
	default:
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to request new pad from template: unsupported track source\npad=%s\ntemplate=%s\nsource=%d", name, templ.GetName(), kind))
		return nil
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	ti := e.Tracks[kind]
	if ti == nil || ti.RtpFilter == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to request new pad from template: no track info found for track source\npad=%s\ntemplate=%s\nsource=%d", name, templ.GetName(), kind))
		return nil
	}

	self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Requesting new pad from template for track source\npad=%s\ntemplate=%s\nsource=%d", name, templ.GetName(), kind))
	recvRtpSrc := e.RtpBin.GetRequestPad(fmt.Sprintf("send_rtp_sink_%d", ti.Kind))
	self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Requested new pad from template for track source: got pad\npad=%s\ntemplate=%s\nsource=%d\ngot_pad=%s", name, templ.GetName(), kind, recvRtpSrc.GetName()))
	if recvRtpSrc == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to request new pad from template: failed to get request pad for RTP source\npad=%s\ntemplate=%s", name, templ.GetName()))
		return nil
	}

	if ret := ti.RtpFilter.GetStaticPad("src").Link(recvRtpSrc); ret != gst.PadLinkOK {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link RTP filter to RTP source\nret=%v", ret))
		return nil
	}

	gpad := gst.NewGhostPadFromTemplate(name, ti.RtpFilter.GetStaticPad("sink"), templ)
	if gpad == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create ghost pad for new pad from template\npad=%s\ntemplate=%s", name, templ.GetName()))
		return nil
	}
	if !self.AddPad(gpad.Pad) {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add ghost pad for new pad from template\npad=%s\ntemplate=%s", name, templ.GetName()))
		return nil
	}
	if !gpad.SetActive(true) {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to activate ghost pad for new pad from template\npad=%s\ntemplate=%s", name, templ.GetName()))
		return nil
	}

	switch kind {
	case livekit.TrackSource_CAMERA:
		self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Created new RTP sink pad for camera track\npad=%s", gpad.GetName()))
	case livekit.TrackSource_SCREEN_SHARE:
		self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Created new RTP sink pad for screen share track\npad=%s", gpad.GetName()))
	case livekit.TrackSource_MICROPHONE:
		self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Created new RTP sink pad for microphone track\npad=%s", gpad.GetName()))
	case livekit.TrackSource_SCREEN_SHARE_AUDIO:
		self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Created new RTP sink pad for screen share audio track\npad=%s", gpad.GetName()))
	}

	return gpad.Pad
}

func (e *SipBin) ReleasePad(instance *gst.Element, pad *gst.Pad) {
	self := gst.ToGstBin(instance)
	templ := pad.GetPadTemplate()
	name := pad.GetName()

	if templ == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to release pad: pad template is nil\npad=%s", name))
		return
	}

	switch templ.GetName() {
	case "send_rtp_sink_%u":
		e.releasePadSendRtpSink(self, pad)
		return
	default:
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to release pad: unrecognized template\npad=%s", name))
		return
	}
}

func (e *SipBin) releasePadSendRtpSink(self *gst.Bin, pad *gst.Pad) {
	name := pad.GetName()
	var session int
	if _, err := fmt.Sscanf(name, "send_rtp_sink_%d", &session); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to parse session number for pad release\npad=%s\nerr=%v", name, err))
		return
	}

	kind := livekit.TrackSource(session)
	switch kind {
	case livekit.TrackSource_CAMERA, livekit.TrackSource_SCREEN_SHARE,
		livekit.TrackSource_MICROPHONE, livekit.TrackSource_SCREEN_SHARE_AUDIO:
	default:
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to release pad: unsupported track source\npad=%s\nsource=%d", name, kind))
		return
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	ti := e.Tracks[kind]
	if ti == nil || ti.RtpFilter == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to release pad: no track info found for track source\npad=%s\nsource=%d", name, kind))
		return
	}

	sink := ti.RtpFilter.GetStaticPad("src").GetPeer()
	if sink != nil && e.RtpBin != nil {
		ti.RtpFilter.GetStaticPad("src").Unlink(sink)
		e.RtpBin.ReleaseRequestPad(sink)
	}

	if !self.RemovePad(pad) {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to remove pad\npad=%s", name))
	}

	switch kind {
	case livekit.TrackSource_CAMERA:
		self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Released RTP sink pad for camera track\npad=%s", name))
	case livekit.TrackSource_SCREEN_SHARE:
		self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Released RTP sink pad for screen share track\npad=%s", name))
	case livekit.TrackSource_MICROPHONE:
		self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Released RTP sink pad for microphone track\npad=%s", name))
	case livekit.TrackSource_SCREEN_SHARE_AUDIO:
		self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Released RTP sink pad for screen share audio track\npad=%s", name))
	}
}
