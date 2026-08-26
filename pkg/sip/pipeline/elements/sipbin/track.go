package sipbin

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/gstsdp"
	"github.com/livekit/protocol/livekit"
	"github.com/pion/rtcp"
)

const keyframeRequestSSRC uint32 = 0xCAFE

const keyframePeriod = 2 * time.Second

type SipTrack struct {
	initialized bool
	Idx         int
	Kind        livekit.TrackSource
	recv        bool
	send        bool
	Proto       string
	Caps        *gst.Caps
	rtpConn     *net.UDPConn
	rtcpConn    *net.UDPConn
	RtpSrc      *gst.Element
	RtcpSrc     *gst.Element
	RtpSink     *gst.Element
	RtcpSink    *gst.Element
	RtpFilter   *gst.Element

	deviceRtcpAddr  *net.UDPAddr
	keyframeMu      sync.Mutex
	lastKeyframeReq time.Time
	firSeq          uint8
	videoSSRC       uint32
	keyframeStop    chan struct{}
	keyframeStarted bool

	linkFeedbackStop    chan struct{}
	linkFeedbackStarted bool

	tmmbrKbps      int
	rxLossTicks    int
	rxCleanTicks   int
	tmmbrOutLast   time.Time
	tmmbrOutActive bool
}

func (e *SipBin) NewTrack(self *gst.Bin, idx int, kind livekit.TrackSource, proto string) (*SipTrack, error) {
	ip := e.bindIP
	if ip == nil {
		ip = e.ip
	}
	if ip == nil {
		return nil, fmt.Errorf("no IP address configured for SIP media")
	}

	if proto == "" {
		proto = "RTP/AVP"
	}

	rtpConn, rtcpConn, err := NewUDPConnPair(e.portStart, e.portEnd, ip)
	if err != nil {
		var fallbackErr error
		rtpConn, rtcpConn, fallbackErr = NewUDPConnPair(e.portStart, e.portEnd, net.IPv4zero)
		if fallbackErr != nil {
			return nil, fmt.Errorf("failed to create UDP connections for SIP media: %w", err)
		}
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to create UDP connections for SIP media, but fallback succeeded\nerr=%v\nfallback_err=%v", err, fallbackErr))
		ip = net.IPv4zero
	}

	grtpSocket, err := GSocketFromUDPConn(rtpConn)
	if err != nil {
		return nil, fmt.Errorf("failed to create GSocket from RTP UDP connection: %w", err)
	}
	grtcpSocket, err := GSocketFromUDPConn(rtcpConn)
	if err != nil {
		return nil, fmt.Errorf("failed to create GSocket from RTCP UDP connection: %w", err)
	}

	bufferSize := 0
	switch kind {
	case livekit.TrackSource_CAMERA, livekit.TrackSource_SCREEN_SHARE:
		bufferSize = 8 * 1024 * 1024 // 8MB for camera and screen share tracks
	}

	rtpSrcCaps := fmt.Sprintf("application/x-rtp, media=(string)%s", kindToMediaType(kind))
	switch kind {
	case livekit.TrackSource_CAMERA, livekit.TrackSource_SCREEN_SHARE:
		rtpSrcCaps += ", rtcp-fb-nack-pli=(boolean)true, rtcp-fb-ccm-fir=(boolean)true"
	}
	rtpSrc, err := gst.NewElementWithProperties("udpsrc", map[string]interface{}{
		"socket":       grtpSocket,
		"close-socket": false,
		"buffer-size":  int(bufferSize),
		"caps":         gst.NewCapsFromString(rtpSrcCaps),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create RTP source element: %w", err)
	}

	rtcpSrc, err := gst.NewElementWithProperties("udpsrc", map[string]interface{}{
		"socket":       grtcpSocket,
		"close-socket": false,
		"caps":         gst.NewCapsFromString("application/x-rtcp"),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create RTCP source element: %w", err)
	}

	rtpSink, err := gst.NewElementWithProperties("udpsink", map[string]interface{}{
		"socket":       grtpSocket,
		"close-socket": false,
		"async":        false,
		"sync":         false,
		"qos":          false,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create RTP sink element: %w", err)
	}

	rtcpSink, err := gst.NewElementWithProperties("udpsink", map[string]interface{}{
		"socket":       grtcpSocket,
		"close-socket": false,
		"async":        false,
		"sync":         false,
		"qos":          false,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create RTCP sink element: %w", err)
	}

	rtpFilter, err := gst.NewElementWithProperties("capsfilter", map[string]interface{}{})
	if err != nil {
		return nil, fmt.Errorf("failed to create RTP filter element: %w", err)
	}

	if err := self.AddMany(rtpSrc, rtcpSrc, rtpSink, rtcpSink, rtpFilter); err != nil {
		return nil, fmt.Errorf("failed to add track elements to bin: %w", err)
	}

	// Lock the udpsinks so the pipeline's state changes skip them until Init()
	// sets the remote host/port and unlocks them.
	for _, sink := range [](*gst.Element){rtpSink, rtcpSink} {
		if err := sink.SetLockedState(true); err != nil {
			return nil, fmt.Errorf("failed to lock sink element state: %w", err)
		}
	}

	return &SipTrack{
		initialized: false,
		Idx:         idx,
		Kind:        kind,
		Proto:       proto,
		rtpConn:     rtpConn,
		rtcpConn:    rtcpConn,
		RtpSrc:      rtpSrc,
		RtcpSrc:     rtcpSrc,
		RtpSink:     rtpSink,
		RtcpSink:    rtcpSink,
		RtpFilter:   rtpFilter,
	}, nil
}

func (t *SipTrack) parseDirection(media *gstsdp.Media) {
	t.recv = true
	t.send = true
	if dir := media.GetAttributeVal("direction"); dir != "" {
		switch dir {
		case "sendonly":
			t.recv = false
		case "recvonly":
			t.send = false
		case "inactive":
			t.recv = false
			t.send = false
		}
	} else if media.HasAttribute("sendonly") {
		t.send = false
	} else if media.HasAttribute("recvonly") {
		t.recv = false
	} else if media.HasAttribute("inactive") {
		t.recv = false
		t.send = false
	}
}

func (t *SipTrack) Init(e *SipBin, self *gst.Bin, media *gstsdp.Media, session *gstsdp.Message, caps *gst.Caps) error {
	if t.initialized {
		return nil
	}

	var conn *gstsdp.Connection
	if media.ConnectionsLen() > 0 {
		conn = media.GetConnection(0)
	} else {
		conn = session.GetConnection()
	}
	if conn == nil {
		return fmt.Errorf("no connection information found in SDP for media index %d", t.Idx)
	}

	t.Caps = caps

	rtcpPort := media.GetPort() + 1
	rtcpAttr := media.GetAttributeVal("rtcp")
	if rtcpAttr != "" {
		if p, err := strconv.Atoi(rtcpAttr); err == nil {
			rtcpPort = uint(p)
		} else {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to parse RTCP port from media attribute\nerr=%v", err))
		}
	}

	host := conn.Address()
	if ip := net.ParseIP(host); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			host = v4.String()
		} else {
			return fmt.Errorf("media %d (kind %d): remote media address %q (sdp addrtype %q) is IPv6; the SIP media stack is IPv4-only", t.Idx, t.Kind, conn.Address(), conn.Addrtype())
		}
	}
	self.Log(CAT, gst.LevelDebug, fmt.Sprintf("track remote media address resolved\ntrack=%d\nkind=%d\naddr=%s\nrtp=%d\nrtcp=%d\nsdp_addrtype=%s\nsdp_address=%s", t.Idx, t.Kind, host, media.GetPort(), rtcpPort, conn.Addrtype(), conn.Address()))

	if addr, raErr := net.ResolveUDPAddr("udp", net.JoinHostPort(host, strconv.Itoa(int(rtcpPort)))); raErr == nil {
		t.deviceRtcpAddr = addr
	} else {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to resolve device RTCP address for keyframe requests\nhost=%s\nrtcp=%d\nerr=%v", host, rtcpPort, raErr))
	}

	if err := errors.Join(
		t.RtpSink.SetProperty("host", host),
		t.RtpSink.SetProperty("port", int(media.GetPort())),
		t.RtcpSink.SetProperty("host", host),
		t.RtcpSink.SetProperty("port", int(rtcpPort)),
		t.RtpFilter.SetProperty("caps", caps),
	); err != nil {
		return fmt.Errorf("failed to set properties on track elements: %w", err)
	}

	sendRtpSink := e.RtpBin.GetRequestPad(fmt.Sprintf("recv_rtp_sink_%d", t.Kind))
	if sendRtpSink == nil {
		return fmt.Errorf("failed to get request pad for RTP sink")
	}
	if ret := t.RtpSrc.GetStaticPad("src").Link(sendRtpSink); ret != gst.PadLinkOK {
		return fmt.Errorf("failed to link RTP source to RTP sink: %v", ret)
	}

	sendRtcpSink := e.RtpBin.GetRequestPad(fmt.Sprintf("recv_rtcp_sink_%d", t.Kind))
	if sendRtcpSink == nil {
		return fmt.Errorf("failed to get request pad for RTCP sink")
	}
	if ret := t.RtcpSrc.GetStaticPad("src").Link(sendRtcpSink); ret != gst.PadLinkOK {
		return fmt.Errorf("failed to link RTCP source to RTCP sink: %v", ret)
	}

	switch t.Kind {
	case livekit.TrackSource_CAMERA, livekit.TrackSource_SCREEN_SHARE:
		t.watchTmmbr()
		t.StartLinkFeedback(e, self)
	}

	sendRtcpSrc := e.RtpBin.GetRequestPad(fmt.Sprintf("send_rtcp_src_%d", t.Kind))
	if sendRtcpSrc == nil {
		return fmt.Errorf("failed to get request pad for RTCP source")
	}
	if ret := sendRtcpSrc.Link(t.RtcpSink.GetStaticPad("sink")); ret != gst.PadLinkOK {
		return fmt.Errorf("failed to link RTCP source to RTCP sink: %v", ret)
	}

	// Unlock the udpsinks locked in NewTrack so SyncStateWithParent below
	// brings them up to the pipeline's state.
	for _, sink := range [](*gst.Element){t.RtpSink, t.RtcpSink} {
		if err := sink.SetLockedState(false); err != nil {
			return fmt.Errorf("failed to unlock sink element %s: %w", sink.GetName(), err)
		}
	}

	var errs []error
	for _, elem := range [](*gst.Element){t.RtpSrc, t.RtcpSrc, t.RtpSink, t.RtcpSink, t.RtpFilter} {
		if !elem.SyncStateWithParent() {
			errs = append(errs, fmt.Errorf("failed to sync state of element %s with parent", elem.GetName()))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("failed to start track: %v", errs)
	}

	t.initialized = true

	self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Initialized track\ntrack=%d\nkind=%d\naddr=%s\nrtp=%d\nrtcp=%d\nsend=%t\nrecv=%t", t.Idx, t.Kind, host, media.GetPort(), rtcpPort, t.send, t.recv))

	return nil
}

func (t *SipTrack) RequestKeyframe(self *gst.Bin, ssrc uint32) {
	if t.rtcpConn == nil || t.deviceRtcpAddr == nil {
		return
	}

	t.keyframeMu.Lock()
	now := time.Now()
	if !t.lastKeyframeReq.IsZero() && now.Sub(t.lastKeyframeReq) < time.Second {
		t.keyframeMu.Unlock()
		return
	}
	t.lastKeyframeReq = now
	t.firSeq++
	firSeq := t.firSeq
	t.keyframeMu.Unlock()

	raw, err := rtcp.Marshal([]rtcp.Packet{
		&rtcp.PictureLossIndication{SenderSSRC: keyframeRequestSSRC, MediaSSRC: ssrc},
		&rtcp.FullIntraRequest{
			SenderSSRC: keyframeRequestSSRC,
			MediaSSRC:  ssrc,
			FIR:        []rtcp.FIREntry{{SSRC: ssrc, SequenceNumber: firSeq}},
		},
	})
	if err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to marshal RTCP keyframe request\nerr=%v", err))
		return
	}

	if _, err := t.rtcpConn.WriteToUDP(raw, t.deviceRtcpAddr); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to send RTCP keyframe request to device\nerr=%v", err))
		return
	}

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Sent RTCP PLI/FIR keyframe request to device\nssrc=%d\nrtcp_addr=%s", ssrc, t.deviceRtcpAddr))
}

func (t *SipTrack) StartPeriodicKeyframe(self *gst.Bin, ssrc uint32) {
	t.keyframeMu.Lock()
	t.videoSSRC = ssrc
	if t.keyframeStarted {
		t.keyframeMu.Unlock()
		return
	}
	t.keyframeStarted = true
	stop := make(chan struct{})
	t.keyframeStop = stop
	t.keyframeMu.Unlock()

	go func() {
		ticker := time.NewTicker(keyframePeriod)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				t.keyframeMu.Lock()
				ssrc := t.videoSSRC
				t.keyframeMu.Unlock()
				if ssrc != 0 {
					t.RequestKeyframe(self, ssrc)
				}
			}
		}
	}()
}

func (t *SipTrack) stopPeriodicKeyframe() {
	t.keyframeMu.Lock()
	if t.keyframeStarted && t.keyframeStop != nil {
		close(t.keyframeStop)
		t.keyframeStop = nil
		t.keyframeStarted = false
	}
	t.keyframeMu.Unlock()
}

func (t *SipTrack) StartLinkFeedback(e *SipBin, self *gst.Bin) {
	t.keyframeMu.Lock()
	if t.linkFeedbackStarted {
		t.keyframeMu.Unlock()
		return
	}
	t.linkFeedbackStarted = true
	stop := make(chan struct{})
	t.linkFeedbackStop = stop
	t.keyframeMu.Unlock()

	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				t.pushLinkFeedback(e, self)
				t.maybeRequestDeviceReduction(e, self)
			}
		}
	}()
}

func (t *SipTrack) stopLinkFeedback() {
	t.keyframeMu.Lock()
	if t.linkFeedbackStarted && t.linkFeedbackStop != nil {
		close(t.linkFeedbackStop)
		t.linkFeedbackStop = nil
		t.linkFeedbackStarted = false
	}
	t.keyframeMu.Unlock()
}

func (t *SipTrack) pushLinkFeedback(e *SipBin, self *gst.Bin) {
	rttMs, fractionLost := e.linkFeedback(t.Kind)

	t.keyframeMu.Lock()
	tmmbrKbps := t.tmmbrKbps
	t.keyframeMu.Unlock()
	budgetKbps := e.sendBudgetKbps(t.Kind)

	if rttMs == 0 && fractionLost == 0 && tmmbrKbps == 0 && budgetKbps == 0 {
		return
	}

	sendPad := self.GetStaticPad(fmt.Sprintf("send_rtp_sink_%d", int(t.Kind)))
	if sendPad == nil {
		return
	}
	peer := sendPad.GetPeer()
	if peer == nil {
		return
	}

	self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Device link feedback\nkind=%d\nfraction_lost=%d\nrtt_ms=%d\ntmmbr_kbps=%d\nbudget_kbps=%d", t.Kind, fractionLost, rttMs, tmmbrKbps, budgetKbps))

	st := gst.NewStructure("vopenia-link-feedback")
	if err := st.SetValue("fraction-lost", int(fractionLost)); err != nil {
		return
	}
	if err := st.SetValue("rtt-ms", rttMs); err != nil {
		return
	}
	if tmmbrKbps > 0 {
		if err := st.SetValue("tmmbr-kbps", tmmbrKbps); err != nil {
			return
		}
	}
	if budgetKbps > 0 {
		if err := st.SetValue("budget-kbps", budgetKbps); err != nil {
			return
		}
	}
	peer.SendEvent(gst.NewCustomEvent(gst.EventTypeCustomUpstream, st.Transfer()))
}

// sendBudgetKbps splits the session-level bandwidth between the outgoing
// video encoders: 70/30 in favor of the slides while screensharing, all for
// the camera otherwise. 128 kbps is reserved for audio and overhead.
func (e *SipBin) sendBudgetKbps(kind livekit.TrackSource) int {
	total := int(e.sessionBudgetKbps.Load())
	if total <= 0 {
		return 0
	}
	avail := total - 128
	if avail < 300 {
		avail = 300
	}
	switch kind {
	case livekit.TrackSource_CAMERA:
		if e.screenshareSending.Load() {
			return avail * 30 / 100
		}
		return avail
	case livekit.TrackSource_SCREEN_SHARE:
		return avail * 70 / 100
	}
	return 0
}

// watchTmmbr parses TMMBR requests from the device's incoming RTCP and
// keeps the latest requested bitrate for the link feedback event.
func (t *SipTrack) watchTmmbr() {
	pad := t.RtcpSrc.GetStaticPad("src")
	if pad == nil {
		return
	}
	pad.AddProbe(gst.PadProbeTypeBuffer, func(_ *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		buf := info.GetBuffer()
		if buf == nil {
			return gst.PadProbeOK
		}
		if kbps := parseTmmbrKbps(buf.Bytes()); kbps > 0 {
			t.keyframeMu.Lock()
			t.tmmbrKbps = kbps
			t.keyframeMu.Unlock()
		}
		return gst.PadProbeOK
	})
}

// parseTmmbrKbps returns the smallest bitrate requested by the TMMBR
// entries of an RTCP compound packet (RFC 5104 §4.2.1), or 0.
func parseTmmbrKbps(data []byte) int {
	best := 0
	for len(data) >= 4 {
		if data[0]>>6 != 2 {
			return best
		}
		length := ((int(data[2])<<8 | int(data[3])) + 1) * 4
		if length > len(data) {
			return best
		}
		if data[1] == 205 && data[0]&0x1f == 3 {
			for off := 12; off+8 <= length; off += 8 {
				fci := binary.BigEndian.Uint32(data[off+4 : off+8])
				exp := fci >> 26
				mantissa := uint64((fci >> 9) & 0x1ffff)
				kbps := int(mantissa << exp / 1000)
				if kbps > 0 && (best == 0 || kbps < best) {
					best = kbps
				}
			}
		}
		data = data[length:]
	}
	return best
}

const tmmbrOutLossThreshold = 13 // fraction-lost units (/256), ~5%

// maybeRequestDeviceReduction sends a TMMBR to the device when our receive
// stream shows sustained loss (85% of the observed bitrate, at most one
// every 3s), and releases the cap after 10 clean seconds.
func (t *SipTrack) maybeRequestDeviceReduction(e *SipBin, self *gst.Bin) {
	st, err := e.getStats(t.Kind)
	if err != nil || st == nil {
		return
	}
	var loss uint8
	var rxBitrate uint64
	for _, src := range st.Sources {
		if src.Internal || src.LastSentRB == nil {
			continue
		}
		if src.LastSentRB.FractionLost > loss {
			loss = src.LastSentRB.FractionLost
		}
		if src.Bitrate > rxBitrate {
			rxBitrate = src.Bitrate
		}
	}

	t.keyframeMu.Lock()
	if loss > tmmbrOutLossThreshold {
		t.rxLossTicks++
		t.rxCleanTicks = 0
	} else {
		t.rxCleanTicks++
		t.rxLossTicks = 0
	}
	degrade := t.rxLossTicks >= 3 && time.Since(t.tmmbrOutLast) >= 3*time.Second
	release := t.tmmbrOutActive && t.rxCleanTicks >= 10
	if degrade {
		t.tmmbrOutLast = time.Now()
		t.tmmbrOutActive = true
	}
	if release {
		t.tmmbrOutActive = false
		t.rxCleanTicks = 0
	}
	ssrc := t.videoSSRC
	t.keyframeMu.Unlock()

	if ssrc == 0 {
		return
	}
	if degrade && rxBitrate > 0 {
		t.sendTmmbr(self, ssrc, rxBitrate*85/100)
	} else if release {
		if capKbps := t.recvCapKbps(); capKbps > 0 {
			t.sendTmmbr(self, ssrc, uint64(capKbps)*1000)
		}
	}
}

func (t *SipTrack) recvCapKbps() int {
	caps := t.Caps
	if caps == nil || caps.IsEmpty() {
		return 0
	}
	s := caps.GetStructureAt(0)
	if s == nil {
		return 0
	}
	if v, err := s.GetString("max-bandwidth"); err == nil {
		if n, convErr := strconv.Atoi(v); convErr == nil {
			return n
		}
	}
	return 0
}

// sendTmmbr sends an RTCP TMMBR (RFC 5104 §4.2.1) asking the device to cap
// its send bitrate to bps.
func (t *SipTrack) sendTmmbr(self *gst.Bin, mediaSSRC uint32, bps uint64) {
	if t.rtcpConn == nil || t.deviceRtcpAddr == nil {
		return
	}

	exp := uint32(0)
	mantissa := bps
	for mantissa >= 1<<17 {
		mantissa >>= 1
		exp++
	}

	raw := make([]byte, 20)
	raw[0] = 0x80 | 3 // V=2, FMT=3 (TMMBR)
	raw[1] = 205      // RTPFB
	binary.BigEndian.PutUint16(raw[2:4], 4)
	binary.BigEndian.PutUint32(raw[4:8], keyframeRequestSSRC)
	binary.BigEndian.PutUint32(raw[8:12], 0)
	binary.BigEndian.PutUint32(raw[12:16], mediaSSRC)
	binary.BigEndian.PutUint32(raw[16:20], exp<<26|uint32(mantissa)<<9)

	if _, err := t.rtcpConn.WriteToUDP(raw, t.deviceRtcpAddr); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to send RTCP TMMBR to device\nerr=%v", err))
		return
	}
	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Sent RTCP TMMBR to device\nssrc=%d\nbps=%d", mediaSSRC, bps))
}

func (e *SipBin) linkFeedback(kind livekit.TrackSource) (rttMs int, fractionLost uint8) {
	st, err := e.getStats(kind)
	if err != nil || st == nil {
		return 0, 0
	}
	var maxRtt uint32
	for _, src := range st.Sources {
		for _, rr := range src.ReceivedRR {
			if rr.RoundTrip > maxRtt {
				maxRtt = rr.RoundTrip
			}
			if rr.FractionLost > fractionLost {
				fractionLost = rr.FractionLost
			}
		}
	}
	return int(uint64(maxRtt) * 1000 / 65536), fractionLost
}

func (t *SipTrack) UpdateCaps(caps *gst.Caps) error {
	t.Caps = caps
	if err := t.RtpFilter.SetProperty("caps", caps); err != nil {
		return fmt.Errorf("failed to update caps on RTP filter: %w", err)
	}
	return nil
}

func (e *SipBin) CleanupTrack(self *gst.Bin, track *SipTrack) error {
	track.stopPeriodicKeyframe()
	track.stopLinkFeedback()

	var errs []error
	for _, elem := range [](*gst.Element){track.RtpSrc, track.RtcpSrc, track.RtpSink, track.RtcpSink, track.RtpFilter} {
		if elem == nil {
			continue
		}
		if err := elem.SetState(gst.StateNull); err != nil {
			errs = append(errs, fmt.Errorf("failed to set state of element %s to null: %w", elem.GetName(), err))
		}

		if err := self.Remove(elem); err != nil {
			errs = append(errs, fmt.Errorf("failed to remove element %s from bin: %w", elem.GetName(), err))
		}
	}
	if track.initialized {
		sendRtpSink := e.RtpBin.GetStaticPad(fmt.Sprintf("recv_rtp_sink_%d", track.Kind))
		if sendRtpSink != nil {
			e.RtpBin.ReleaseRequestPad(sendRtpSink)
		}
		sendRtcpSrc := e.RtpBin.GetStaticPad(fmt.Sprintf("send_rtcp_src_%d", track.Kind))
		if sendRtcpSrc != nil {
			e.RtpBin.ReleaseRequestPad(sendRtcpSrc)
		}
		recvRtpSrc := e.RtpBin.GetStaticPad(fmt.Sprintf("send_rtp_sink_%d", track.Kind))
		if recvRtpSrc != nil {
			e.RtpBin.ReleaseRequestPad(recvRtpSrc)
		}
		recvRtcpSink := e.RtpBin.GetStaticPad(fmt.Sprintf("recv_rtcp_sink_%d", track.Kind))
		if recvRtcpSink != nil {
			e.RtpBin.ReleaseRequestPad(recvRtcpSink)
		}
	}
	if track.rtpConn != nil {
		if err := track.rtpConn.Close(); err != nil {
			errs = append(errs, fmt.Errorf("failed to close RTP UDP connection: %w", err))
		}
	}
	if track.rtcpConn != nil {
		if err := track.rtcpConn.Close(); err != nil {
			errs = append(errs, fmt.Errorf("failed to close RTCP UDP connection: %w", err))
		}
	}

	e.Tracks[track.Kind] = nil
	e.PtMap[track.Kind] = make(map[uint8]*gst.Caps)
	track.initialized = false

	if len(errs) > 0 {
		return fmt.Errorf("failed to cleanup track: %v", errs)
	}

	return nil
}

func (e *SipBin) trackToggleEvent(self *gst.Bin, kind livekit.TrackSource, on bool) error {
	switch kind {
	case livekit.TrackSource_CAMERA, livekit.TrackSource_MICROPHONE, livekit.TrackSource_SCREEN_SHARE, livekit.TrackSource_SCREEN_SHARE_AUDIO:
	default:
		return fmt.Errorf("invalid track source kind: %s", kind)
	}

	track := e.Tracks[kind]
	if track == nil || !track.initialized {
		return nil
	}

	var st *gst.Structure
	if on {
		st = gst.NewStructure(EventOOBStreamOn)
	} else {
		st = gst.NewStructure(EventOOBStreamOff)
	}

	trackPad := track.RtpSrc.GetStaticPad("src")
	if trackPad == nil {
		return fmt.Errorf("failed to get RTP source pad for track source %s", kind)
	}

	event := gst.NewCustomEvent(gst.EventTypeCustomOOB, st.Transfer())
	if !trackPad.PushEvent(event) {
		return fmt.Errorf("failed to push event to track pad for track source %s", kind)
	}

	return nil
}

func (e *SipBin) clearTrack(self *gst.Bin, kind livekit.TrackSource) {
	if e.Tracks[kind] == nil {
		return
	}

	rtpSessionVal, err := e.RtpBin.Emit("get-internal-session", uint(kind))
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get internal session for track source\nsource=%s\nerr=%v", kind, err))
		self.Error("Failed to get internal session for track source", err)
		return
	}
	rtpSession, ok := rtpSessionVal.(*glib.Object)
	if !ok {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to convert internal session to element for track source\nsource=%s", kind))
		self.Error("Failed to convert internal session to element for track source", fmt.Errorf("invalid RTP session element"))
		return
	}

	sourcesVal, err := rtpSession.GetProperty("sources")
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get sources property from RTP session\nerr=%v", err))
		self.Error("Failed to get sources property from RTP session", err)
		return
	}
	sources, ok := sourcesVal.(*glib.ValueArray)
	if !ok {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to convert sources property to value array for track source\nsource=%s", kind))
		self.Error("Failed to convert sources property to value array for track source", fmt.Errorf("invalid sources property"))
		return
	}
	ssrcs := make([]uint32, 0, sources.Len())
	nptk := make([]uint64, 0, sources.Len())
	for i := range sources.Len() {
		rtpSourceVal, err := sources.Index(i)
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get source from sources array for track source\nindex=%d\nsource=%s\nerr=%v", i, kind, err))
			self.Error(fmt.Sprintf("Failed to get source at index %d from sources array for track source %s", i, kind), err)
			continue
		}
		rtpSource, ok := rtpSourceVal.(*glib.Object)
		if !ok {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to convert source to element for track source\nindex=%d\nsource=%s", i, kind))
			self.Error(fmt.Sprintf("Failed to convert source at index %d to element for track source %s", i, kind), fmt.Errorf("invalid RTP source element"))
			continue
		}

		statsVal, err := rtpSource.GetProperty("stats")
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get stats property from RTP source for track source\nindex=%d\nsource=%s\nerr=%v", i, kind, err))
			self.Error(fmt.Sprintf("Failed to get stats property from RTP source at index %d for track source %s", i, kind), err)
			continue
		}
		stats, ok := statsVal.(*gst.Structure)
		if !ok {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to convert stats property to structure for RTP source for track source\nindex=%d\nsource=%s", i, kind))
			self.Error(fmt.Sprintf("Failed to convert stats property to structure for RTP source at index %d for track source %s", i, kind), fmt.Errorf("invalid stats property"))
			continue
		}
		internal, err := stats.GetBool("internal")
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get internal field from stats for RTP source for track source\nindex=%d\nsource=%s\nerr=%v", i, kind, err))
			self.Error(fmt.Sprintf("Failed to get internal field from stats for RTP source at index %d for track source %s", i, kind), err)
			continue
		}
		if internal {
			continue
		}
		isCsrc, err := stats.GetBool("is-csrc")
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get is-csrc field from stats for RTP source for track source\nindex=%d\nsource=%s\nerr=%v", i, kind, err))
			self.Error(fmt.Sprintf("Failed to get is-csrc field from stats for RTP source at index %d for track source %s", i, kind), err)
			continue
		}
		if isCsrc {
			continue
		}
		validated, err := stats.GetBool("validated")
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get validated field from stats for RTP source for track source\nindex=%d\nsource=%s\nerr=%v", i, kind, err))
			self.Error(fmt.Sprintf("Failed to get validated field from stats for RTP source at index %d for track source %s", i, kind), err)
			continue
		}
		if !validated {
			continue
		}

		ssrcVal, err := rtpSource.GetProperty("ssrc")
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get ssrc property from RTP source for track source\nindex=%d\nsource=%s\nerr=%v", i, kind, err))
			self.Error(fmt.Sprintf("Failed to get ssrc property from RTP source at index %d for track source %s", i, kind), err)
			continue
		}
		ssrc, ok := ssrcVal.(uint)
		if !ok {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to convert ssrc property to uint for RTP source for track source\nindex=%d\nsource=%s", i, kind))
			self.Error(fmt.Sprintf("Failed to convert ssrc property to uint for RTP source at index %d for track source %s", i, kind), fmt.Errorf("invalid ssrc property"))
			continue
		}

		packetsReceived, err := stats.GetUint64("packets-received")
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get packets-received field from stats for RTP source for track source\nindex=%d\nsource=%s\nerr=%v", i, kind, err))
			self.Error(fmt.Sprintf("Failed to get packets-received field from stats for RTP source at index %d for track source %s", i, kind), err)
			continue
		}
		if packetsReceived == 0 {
			continue
		}
		ssrcs = append(ssrcs, uint32(ssrc))
		nptk = append(nptk, packetsReceived)
	}
	if len(ssrcs) == 0 {
		return
	}
	time.Sleep(500 * time.Millisecond)
	self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Clearing SSRCs from RTP session for track source\ncount=%d\nsource=%s\nssrcs=%v", len(ssrcs), kind, ssrcs))
	for i, ssrc := range ssrcs {
		rtpSourceVal, err := rtpSession.Emit("get-source-by-ssrc", uint(ssrc))
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get source by SSRC from RTP session\nssrc=%d\nerr=%v", ssrc, err))
			self.Error(fmt.Sprintf("Failed to get source by SSRC %d from RTP session", ssrc), err)
			continue
		}
		rtpSource, ok := rtpSourceVal.(*glib.Object)
		if !ok || rtpSource == nil {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("No source found for SSRC in RTP session\nssrc=%d", ssrc))
			continue
		}

		statsVal, err := rtpSource.GetProperty("stats")
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get stats property from RTP source for SSRC\nssrc=%d\nerr=%v", ssrc, err))
			self.Error(fmt.Sprintf("Failed to get stats property from RTP source for SSRC %d", ssrc), err)
			continue
		}
		stats, ok := statsVal.(*gst.Structure)
		if !ok {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to convert stats property to structure for RTP source for SSRC\nssrc=%d", ssrc))
			self.Error(fmt.Sprintf("Failed to convert stats property to structure for RTP source for SSRC %d", ssrc), fmt.Errorf("invalid stats property"))
			continue
		}
		packetsReceived, err := stats.GetUint64("packets-received")
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get packets-received field from stats for RTP source for SSRC\nssrc=%d\nerr=%v", ssrc, err))
			self.Error(fmt.Sprintf("Failed to get packets-received field from stats for RTP source for SSRC %d", ssrc), err)
			continue
		}

		if packetsReceived > nptk[i] {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Source is still receiving packets, skipping clear\nssrc=%d\npackets_received=%d\nprev_packets_received=%d", ssrc, packetsReceived, nptk[i]))
			continue
		}

		if _, err := e.RtpBin.Emit("clear-ssrc", uint(kind), ssrc); err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to clear ssrc from rtpbin for track source\nssrc=%d\nsource=%s\nerr=%v", ssrc, kind, err))
			self.Error(fmt.Sprintf("Failed to clear ssrc %d from rtpbin for track source %s", ssrc, kind), err)
		}
	}
}
