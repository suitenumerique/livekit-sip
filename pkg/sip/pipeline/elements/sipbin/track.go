package sipbin

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/gstsdp"
	"github.com/livekit/protocol/livekit"
)

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
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to create UDP connections for SIP media: %v, but fallback succeeded: %v", err, fallbackErr))
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
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to parse RTCP port from media attribute: %v", err))
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
	self.Log(CAT, gst.LevelDebug, fmt.Sprintf("track %d kind=%d remote media addr=%s rtp=%d rtcp=%d (sdp addrtype=%s address=%s)", t.Idx, t.Kind, host, media.GetPort(), rtcpPort, conn.Addrtype(), conn.Address()))

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

	self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Initialized track %d (kind %d) with remote media address %s and ports RTP=%d RTCP=%d send=%t recv=%t", t.Idx, t.Kind, host, media.GetPort(), rtcpPort, t.send, t.recv))

	return nil
}

func (t *SipTrack) UpdateCaps(caps *gst.Caps) error {
	t.Caps = caps
	if err := t.RtpFilter.SetProperty("caps", caps); err != nil {
		return fmt.Errorf("failed to update caps on RTP filter: %w", err)
	}
	return nil
}

func (e *SipBin) CleanupTrack(self *gst.Bin, track *SipTrack) error {
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
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get internal session for track source %s: %v", kind, err))
		self.Error("Failed to get internal session for track source", err)
		return
	}
	rtpSession, ok := rtpSessionVal.(*glib.Object)
	if !ok {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to convert internal session to element for track source %s", kind))
		self.Error("Failed to convert internal session to element for track source", fmt.Errorf("invalid RTP session element"))
		return
	}

	sourcesVal, err := rtpSession.GetProperty("sources")
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get sources property from RTP session: %v", err))
		self.Error("Failed to get sources property from RTP session", err)
		return
	}
	sources, ok := sourcesVal.(*glib.ValueArray)
	if !ok {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to convert sources property to value array for track source %s", kind))
		self.Error("Failed to convert sources property to value array for track source", fmt.Errorf("invalid sources property"))
		return
	}
	ssrcs := make([]uint32, 0, sources.Len())
	nptk := make([]uint64, 0, sources.Len())
	for i := range sources.Len() {
		rtpSourceVal, err := sources.Index(i)
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get source at index %d from sources array for track source %s: %v", i, kind, err))
			self.Error(fmt.Sprintf("Failed to get source at index %d from sources array for track source %s", i, kind), err)
			continue
		}
		rtpSource, ok := rtpSourceVal.(*glib.Object)
		if !ok {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to convert source at index %d to element for track source %s", i, kind))
			self.Error(fmt.Sprintf("Failed to convert source at index %d to element for track source %s", i, kind), fmt.Errorf("invalid RTP source element"))
			continue
		}

		statsVal, err := rtpSource.GetProperty("stats")
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get stats property from RTP source at index %d for track source %s: %v", i, kind, err))
			self.Error(fmt.Sprintf("Failed to get stats property from RTP source at index %d for track source %s", i, kind), err)
			continue
		}
		stats, ok := statsVal.(*gst.Structure)
		if !ok {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to convert stats property to structure for RTP source at index %d for track source %s", i, kind))
			self.Error(fmt.Sprintf("Failed to convert stats property to structure for RTP source at index %d for track source %s", i, kind), fmt.Errorf("invalid stats property"))
			continue
		}
		internal, err := stats.GetBool("internal")
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get internal field from stats for RTP source at index %d for track source %s: %v", i, kind, err))
			self.Error(fmt.Sprintf("Failed to get internal field from stats for RTP source at index %d for track source %s", i, kind), err)
			continue
		}
		if internal {
			continue
		}
		isCsrc, err := stats.GetBool("is-csrc")
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get is-csrc field from stats for RTP source at index %d for track source %s: %v", i, kind, err))
			self.Error(fmt.Sprintf("Failed to get is-csrc field from stats for RTP source at index %d for track source %s", i, kind), err)
			continue
		}
		if isCsrc {
			continue
		}
		validated, err := stats.GetBool("validated")
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get validated field from stats for RTP source at index %d for track source %s: %v", i, kind, err))
			self.Error(fmt.Sprintf("Failed to get validated field from stats for RTP source at index %d for track source %s", i, kind), err)
			continue
		}
		if !validated {
			continue
		}

		ssrcVal, err := rtpSource.GetProperty("ssrc")
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get ssrc property from RTP source at index %d for track source %s: %v", i, kind, err))
			self.Error(fmt.Sprintf("Failed to get ssrc property from RTP source at index %d for track source %s", i, kind), err)
			continue
		}
		ssrc, ok := ssrcVal.(uint)
		if !ok {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to convert ssrc property to uint for RTP source at index %d for track source %s", i, kind))
			self.Error(fmt.Sprintf("Failed to convert ssrc property to uint for RTP source at index %d for track source %s", i, kind), fmt.Errorf("invalid ssrc property"))
			continue
		}

		packetsReceived, err := stats.GetUint64("packets-received")
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get packets-received field from stats for RTP source at index %d for track source %s: %v", i, kind, err))
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
	self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Clearing %d SSRCs from RTP session for track source %s: %v", len(ssrcs), kind, ssrcs))
	for i, ssrc := range ssrcs {
		rtpSourceVal, err := rtpSession.Emit("get-source-by-ssrc", uint(ssrc))
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get source by SSRC %d from RTP session: %v", ssrc, err))
			self.Error(fmt.Sprintf("Failed to get source by SSRC %d from RTP session", ssrc), err)
			continue
		}
		rtpSource, ok := rtpSourceVal.(*glib.Object)
		if !ok || rtpSource == nil {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("No source found for SSRC %d in RTP session", ssrc))
			continue
		}

		statsVal, err := rtpSource.GetProperty("stats")
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get stats property from RTP source for SSRC %d: %v", ssrc, err))
			self.Error(fmt.Sprintf("Failed to get stats property from RTP source for SSRC %d", ssrc), err)
			continue
		}
		stats, ok := statsVal.(*gst.Structure)
		if !ok {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to convert stats property to structure for RTP source for SSRC %d", ssrc))
			self.Error(fmt.Sprintf("Failed to convert stats property to structure for RTP source for SSRC %d", ssrc), fmt.Errorf("invalid stats property"))
			continue
		}
		packetsReceived, err := stats.GetUint64("packets-received")
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get packets-received field from stats for RTP source for SSRC %d: %v", ssrc, err))
			self.Error(fmt.Sprintf("Failed to get packets-received field from stats for RTP source for SSRC %d", ssrc), err)
			continue
		}

		if packetsReceived > nptk[i] {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Source for SSRC %d is still receiving packets (received %d, previously received %d), skipping clear", ssrc, packetsReceived, nptk[i]))
			continue
		}

		if _, err := e.RtpBin.Emit("clear-ssrc", uint(kind), ssrc); err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to clear ssrc %d from rtpbin for track source %s: %v", ssrc, kind, err))
			self.Error(fmt.Sprintf("Failed to clear ssrc %d from rtpbin for track source %s", ssrc, kind), err)
		}
	}
}
