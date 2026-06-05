package livekitbin

import (
	"errors"
	"fmt"
	"strings"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/livekitbin/livekittracks"
	"github.com/pion/webrtc/v4"
)

const QDataPadPeerRef = "livekitbin-pad-peer-ref"
const QDataSessionID = "livekitbin-session-id"

func (p *LivekitBinPublication) Init(e *LivekitBin, self *gst.Bin, kind livekit.TrackSource, pad *gst.Pad) error {
	if p == nil {
		return fmt.Errorf("publication is nil")
	}

	if p.initialized {
		return nil
	}

	var mimeType string
	switch kind {
	case livekit.TrackSource_MICROPHONE:
		mimeType = e.config.microphoneMimeType
	case livekit.TrackSource_CAMERA:
		mimeType = e.config.cameraMimeType
	case livekit.TrackSource_SCREEN_SHARE:
		mimeType = e.config.screenshareMimeType
	case livekit.TrackSource_SCREEN_SHARE_AUDIO:
		mimeType = e.config.screenshareAudioMimeType
	default:
		return fmt.Errorf("unknown track source")
	}

	track, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{
		MimeType: mimeType,
	}, kind.String(), "pion")
	if err != nil {
		return fmt.Errorf("failed to create new local track: %w", err)
	}

	// pub, err := e.room.LocalParticipant.PublishTrack(track, &lksdk.TrackPublicationOptions{
	// 	Name:   fmt.Sprintf("%s_%s", e.room.LocalParticipant.Identity(), kind.String()),
	// 	Source: kind,
	// })
	// if err != nil {
	// 	return fmt.Errorf("failed to create sink track: %w", err)
	// }

	// self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Published camera track with SID %s", pub.SID()))

	queue, err := gst.NewElementWithProperties("queue", map[string]interface{}{})
	if err != nil {
		return fmt.Errorf("failed to create track queue element: %w", err)
	}

	element, err := gst.NewElementWithProperties("livekitbin_sinktrack", map[string]interface{}{
		"track": glib.ArbitraryValue{Data: track},
		// "pub":   glib.ArbitraryValue{Data: pub},
		"opts": glib.ArbitraryValue{Data: &lksdk.TrackPublicationOptions{
			Name:   fmt.Sprintf("%s_%s", e.room.LocalParticipant.Identity(), kind.String()),
			Source: kind,
		}},
		"lp": glib.ArbitraryValue{Data: e.room.LocalParticipant},
	})
	if err != nil {
		return fmt.Errorf("failed to create sink track element: %w", err)
	}
	if err := self.AddMany(queue, element); err != nil {
		return fmt.Errorf("failed to add sink track element to bin: %w", err)
	}

	if err := queue.Link(element); err != nil {
		return fmt.Errorf("failed to link track queue to sink track element: %w", err)
	}

	if !queue.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to sync state with parent for sink track queue element: %v", err))
	}

	if !element.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to sync state with parent for sink track element: %v", err))
	}

	if ret := pad.Link(queue.GetStaticPad("sink")); ret != gst.PadLinkOK {
		return fmt.Errorf("failed to link pad to sink track element: %v", ret)
	}

	p.TrackQueue = queue
	p.TrackSink = element
	p.initialized = true

	return nil
}

func (e *LivekitBin) onRtpBinPadAddedSendRtp(self *gst.Bin, pad *gst.Pad, pname string) {
	// sync operation so lock is already held

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Publishing track with pad name: %s", pname))

	var session int
	_, err := fmt.Sscanf(pname, "send_rtp_src_%d", &session)
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error parsing pad name %s: %v", pname, err))
		return
	}

	kind := livekit.TrackSource(session)
	switch kind {
	case livekit.TrackSource_MICROPHONE,
		livekit.TrackSource_CAMERA,
		livekit.TrackSource_SCREEN_SHARE,
		livekit.TrackSource_SCREEN_SHARE_AUDIO:
	default:
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Unknown track source in pad name: %s", pname))
		self.Error(fmt.Sprintf("Unknown track source in pad name: %s", pname), nil)
		return
	}

	if err := e.publications[kind].Init(e, self, kind, pad); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error initializing publication for track source %s: %v", kind.String(), err))
		self.Error(fmt.Sprintf("Error initializing publication for track source %s", kind.String()), err)
		return
	}

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Added sink track for pad name: %s", pname))
}

func (r *LivekitBinRtcp) Init(e *LivekitBin, self *gst.Bin) error {
	if r.initialized {
		return nil
	}

	pc := e.room.LocalParticipant.GetPublisherPeerConnection()
	if pc == nil {
		return fmt.Errorf("publisher PeerConnection is nil")
	}

	var err error

	r.RtcpFunnel, err = gst.NewElementWithName("funnel", "rtcp_funnel")
	if err != nil {
		return fmt.Errorf("failed to create RTCP funnel element: %w", err)
	}

	r.SinkRtcp, err = gst.NewElementWithProperties("livekitbin_sinkrtcp", map[string]interface{}{
		"pc": glib.ArbitraryValue{Data: pc},
	})
	if err != nil {
		return fmt.Errorf("failed to create RTCP sink funnel element: %w", err)
	}

	if err := self.AddMany(r.RtcpFunnel, r.SinkRtcp); err != nil {
		return fmt.Errorf("failed to add RTCP funnel elements to bin: %w", err)
	}

	if err := r.RtcpFunnel.Link(r.SinkRtcp); err != nil {
		return fmt.Errorf("failed to link RTCP funnel to RTCP sink: %w", err)
	}

	if !r.RtcpFunnel.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to sync state with parent for RTCP funnel: %v", err))
	}
	if !r.SinkRtcp.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to sync state with parent for RTCP sink funnel: %v", err))
	}

	r.initialized = true

	return nil
}

func (r *LivekitBinRtcp) LinkKind(e *LivekitBin, self *gst.Bin, kind livekit.TrackSource) error {
	if err := r.Init(e, self); err != nil {
		return fmt.Errorf("failed to initialize RTCP funnel: %w", err)
	}

	switch kind {
	case livekit.TrackSource_MICROPHONE,
		livekit.TrackSource_CAMERA,
		livekit.TrackSource_SCREEN_SHARE,
		livekit.TrackSource_SCREEN_SHARE_AUDIO:
	default:
		return fmt.Errorf("unknown track source: %s", kind.String())
	}

	if r.sessions[kind] {
		return nil
	}

	rtcpSrc := e.RtpBin.GetRequestPad(fmt.Sprintf("send_rtcp_src_%d", int(kind)))
	if rtcpSrc == nil {
		return fmt.Errorf("failed to get RTCP source pad for track source %s: pad not found", kind.String())
	}

	dstPad := r.RtcpFunnel.GetRequestPad(fmt.Sprintf("sink_%d", int(kind)))
	if dstPad == nil {
		return fmt.Errorf("failed to get RTCP funnel sink pad for track source %s: pad not found", kind.String())
	}

	if ret := rtcpSrc.Link(dstPad); ret != gst.PadLinkOK {
		return fmt.Errorf("failed to link RTCP source pad to RTCP funnel for track source %s: %v", kind.String(), ret)
	}

	r.sessions[kind] = true

	return nil
}

func mimeTypeToCaps(mimeType string) *gst.Caps {
	media, enc, ok := strings.Cut(mimeType, "/")
	if !ok {
		return nil
	}
	capsStr := fmt.Sprintf("application/x-rtp, media=%s, encoding-name=%s, rtcp-fb-nack-pli=(boolean)true, rtcp-fb-ccm-fir=(boolean)true", strings.ToLower(media), strings.ToUpper(enc))
	return gst.NewCapsFromString(capsStr)
}

func (e *LivekitBin) requestNewPadSendRtp(instance *gst.Element, templ *gst.PadTemplate, name string, _ *gst.Caps) *gst.Pad {
	self := gst.ToGstBin(instance)

	if name == "" {
		self.Log(CAT, gst.LevelError, "Requested pad name is empty")
		return nil
	}

	var session int
	if _, err := fmt.Sscanf(name, "send_rtp_sink_%d", &session); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error parsing requested pad name %s: %v", name, err))
		return nil
	}

	kind := livekit.TrackSource(session)

	e.mu.Lock()
	defer e.mu.Unlock()

	pub := e.publications[kind]
	if pub != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Publication already exists for track source %s when requesting pad name %s", kind.String(), name))
		self.Error(fmt.Sprintf("Publication already exists for track source %s when requesting pad name %s", kind.String(), name), fmt.Errorf("publication error"))
		return nil
	}

	var caps *gst.Caps
	switch kind {
	case livekit.TrackSource_MICROPHONE:
		caps = mimeTypeToCaps(e.config.microphoneMimeType)
	case livekit.TrackSource_CAMERA:
		caps = mimeTypeToCaps(e.config.cameraMimeType)
	case livekit.TrackSource_SCREEN_SHARE:
		caps = mimeTypeToCaps(e.config.screenshareMimeType)
	case livekit.TrackSource_SCREEN_SHARE_AUDIO:
		caps = mimeTypeToCaps(e.config.screenshareAudioMimeType)
	default:
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Unknown track source in pad name: %s", name))
		return nil
	}

	if caps == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create caps for requested pad name %s. configured mime type: [%s]", name, strings.Join([]string{e.config.microphoneMimeType, e.config.cameraMimeType, e.config.screenshareMimeType, e.config.screenshareAudioMimeType}, ", ")))
		self.Error(fmt.Sprintf("Failed to create caps for requested pad name %s", name), fmt.Errorf("caps error"))
		return nil
	}

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Forwarding publish track with session: %d", session))

	pub = &LivekitBinPublication{}
	var err error
	pub.FormatFilter, err = gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": caps,
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create capsfilter element for requested pad name %s: %v", name, err))
		self.Error(fmt.Sprintf("Failed to create capsfilter element for requested pad name %s", name), err)
		return nil
	}

	if err := self.Add(pub.FormatFilter); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add capsfilter element for requested pad name %s: %v", name, err))
		self.Error(fmt.Sprintf("Failed to add capsfilter element for requested pad name %s", name), err)
		return nil
	}

	if !pub.FormatFilter.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to sync state with parent for capsfilter element for requested pad name %s: %v", name, err))
	}

	e.publications[kind] = pub

	pad := pub.FormatFilter.GetStaticPad("sink")

	if !e.Is(RoomStateJoined) {
		pub.probeID = pad.AddProbe(gst.PadProbeTypeBuffer, livekittracks.PadProbeDrop)
		e.wg.Add(1)
		go e.deferLinkPadSendRtp(kind)
	} else {
		if err := e.linkPadSendRtp(self, pub, kind); err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error linking pad for track source %s: %v", kind.String(), err))
			self.Error(fmt.Sprintf("Error linking pad for track source %s", kind.String()), err)
			return nil
		}
		self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Linked pad for track source %s immediately since room is already joined", kind.String()))
	}

	class := gst.ToElementClass(self.Class())

	gpad := gst.NewGhostPadFromTemplate(name, pad, class.GetPadTemplate("send_rtp_sink_%u"))
	if gpad == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error creating ghost pad for requested pad name %s", name))
		self.Error(fmt.Sprintf("Error creating ghost pad for requested pad name %s", name), fmt.Errorf("ghost pad error"))
		return nil
	}
	if !self.AddPad(gpad.Pad) {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error adding ghost pad for requested pad name %s", name))
		self.Error(fmt.Sprintf("Error adding ghost pad for requested pad name %s", name), fmt.Errorf("ghost pad error"))
		return nil
	}

	if !gpad.SetActive(true) {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to activate ghost pad for name: %s", name))
		self.Error(fmt.Sprintf("Failed to activate ghost pad for name: %s", name), fmt.Errorf("ghost pad error"))
		return nil
	}

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Created ghost pad for requested pad name: %s", name))

	return gpad.Pad
}

func (e *LivekitBin) deferLinkPadSendRtp(kind livekit.TrackSource) {
	defer e.wg.Done()
	if err := e.Wait(RoomStateJoined); err != nil {
		if errors.Is(err, ErrRoomClosed) {
			return
		}

		self := gst.ToGstBin(e.self.Get())
		if self != nil && self.Instance() != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error waiting for room to be joined while linking pad for track source %s: %v", kind.String(), err))
			self.Error(fmt.Sprintf("Error waiting for room to be joined while linking pad for track source %s", kind.String()), err)
		}
		return
	}
	self := gst.ToGstBin(e.self.Get())
	if self == nil || self.Instance() == nil {
		return
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	pub := e.publications[kind]
	if pub == nil || pub.initialized {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Invalid publication state for track source %s while linking pad: publication is nil or already initialized", kind.String()))
		return
	}

	if err := e.linkPadSendRtp(self, pub, kind); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error linking pad for track source %s: %v", kind.String(), err))
		self.Error(fmt.Sprintf("Error linking pad for track source %s", kind.String()), err)
		return
	}

	if pub.probeID != 0 {
		pub.FormatFilter.GetStaticPad("sink").RemoveProbe(pub.probeID)
		pub.probeID = 0
		self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Removed buffer drop probe for track source %s after linking pad", kind.String()))
	}

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Linked pad for track source %s after room joined", kind.String()))
}

func (e *LivekitBin) linkPadSendRtp(self *gst.Bin, pub *LivekitBinPublication, kind livekit.TrackSource) error {
	pad := e.RtpBin.GetRequestPad(fmt.Sprintf("send_rtp_sink_%d", int(kind)))
	if pad == nil {
		return fmt.Errorf("failed to get request pad for track source %s: pad not found", kind.String())
	}

	if ret := pub.FormatFilter.GetStaticPad("src").Link(pad); ret != gst.PadLinkOK {
		return fmt.Errorf("failed to link format filter to RTP bin for track source %s: link error: %v", kind.String(), ret)
	}

	if err := e.rtcp.LinkKind(e, self, kind); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to link RTCP funnel for track source %s: %v", kind.String(), err))
	}

	return nil
}

func (e *LivekitBin) capsFromTrack(self *gst.Bin, track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) (*gst.Caps, uint8) {
	pt := track.PayloadType()
	capsStr := fmt.Sprintf("application/x-rtp, media=%s, payload=%d", track.Kind().String(), pt)

	_, enc, ok := strings.Cut(track.Codec().MimeType, "/")
	if !ok {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Invalid codec mime type for track %s: %s", track.ID(), track.Codec().MimeType))
	} else {
		capsStr += fmt.Sprintf(", encoding-name=%s", strings.ToUpper(enc))
	}
	codec := track.Codec()

	capsStr += fmt.Sprintf(", clock-rate=%d", codec.ClockRate)

	if codec.Channels > 0 {
		capsStr += fmt.Sprintf(", channels=%d", codec.Channels)
	}

	capsStr += ", rtcp-fb-nack-pli=(boolean)true, rtcp-fb-ccm-fir=(boolean)true"

	return gst.NewCapsFromString(capsStr), uint8(pt)
}

func (f *LivekitBinTrackFunnel) Init(e *LivekitBin, self *gst.Bin, kind livekit.TrackSource) error {
	if f.initialized {
		return nil
	}

	var err error
	f.RtpFunnel, err = gst.NewElementWithName("rtpfunnel", fmt.Sprintf("%s_rtp_funnel", kind.String()))
	if err != nil {
		return fmt.Errorf("failed to create RTP funnel element for track source %s: %w", kind.String(), err)
	}
	f.RtcpFunnel, err = gst.NewElementWithName("funnel", fmt.Sprintf("%s_rtcp_funnel", kind.String()))
	if err != nil {
		return fmt.Errorf("failed to create RTCP funnel element for track source %s: %w", kind.String(), err)
	}

	if err := self.AddMany(f.RtpFunnel, f.RtcpFunnel); err != nil {
		return fmt.Errorf("failed to add funnel elements to bin for track source %s: %w", kind.String(), err)
	}

	rtpPad := e.RtpBin.GetRequestPad(fmt.Sprintf("recv_rtp_sink_%d", int(kind)))
	if rtpPad == nil {
		return fmt.Errorf("failed to get RTP bin request pad for track source %s: pad not found", kind.String())
	}
	if ret := f.RtpFunnel.GetStaticPad("src").Link(rtpPad); ret != gst.PadLinkOK {
		return fmt.Errorf("failed to link RTP funnel to RTP bin for track source %s: link error: %v", kind.String(), ret)
	}

	rtcpPad := e.RtpBin.GetRequestPad(fmt.Sprintf("recv_rtcp_sink_%d", int(kind)))
	if rtcpPad == nil {
		return fmt.Errorf("failed to get RTP bin request pad for track source %s: pad not found", kind.String())
	}
	if ret := f.RtcpFunnel.GetStaticPad("src").Link(rtcpPad); ret != gst.PadLinkOK {
		return fmt.Errorf("failed to link RTCP funnel to RTP bin for track source %s: link error: %v", kind.String(), ret)
	}

	if !f.RtpFunnel.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to sync state with parent for RTP funnel of track source %s: %v", kind.String(), err))
	}
	if !f.RtcpFunnel.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to sync state with parent for RTCP funnel of track source %s: %v", kind.String(), err))
	}

	f.initialized = true

	return nil
}

func (e *LivekitBin) SubscribeTrack(track *webrtc.TrackRemote, pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
	self := gst.ToGstBin(e.self.Get())
	if self == nil || self.Instance() == nil {
		return
	}

	kind := pub.Source()
	var funnel *LivekitBinTrackFunnel
	switch kind {
	case livekit.TrackSource_CAMERA:
		funnel = &e.funnels[kind]
	case livekit.TrackSource_MICROPHONE:
		funnel = &e.funnels[kind]
	case livekit.TrackSource_SCREEN_SHARE:
		funnel = &e.funnels[kind]
	case livekit.TrackSource_SCREEN_SHARE_AUDIO:
		funnel = &e.funnels[kind]
	default:
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Unknown track source for track %s: %d", track.ID(), pub.Source()))
		return
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	srcTrack, ok := e.tracks[pub.SID()]
	if ok {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Track with SID %s already exists, skipping subscription for track ID %s", pub.SID(), track.ID()))
		pub.SetSubscribed(false)
		return
	}

	if err := funnel.Init(e, self, kind); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error initializing funnel for track source %s: %v", kind.String(), err))
		self.Error(fmt.Sprintf("Error initializing funnel for track source %s", kind.String()), err)
		return
	}

	if err := e.rtcp.LinkKind(e, self, kind); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to link RTCP funnel for track source %s: %v", kind.String(), err))
	}

	caps, pt := e.capsFromTrack(self, track, pub, rp)

	e.ptMu.Lock()
	if existing, exist := e.PtMap[kind][pt]; exist && !existing.IsEqual(caps) {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Overwriting existing caps for payload type %d: old caps: %s, new caps: %s", pt, existing.String(), caps.String()))
	}
	e.PtMap[kind][pt] = caps
	e.ptMu.Unlock()

	element, err := gst.NewElementWithProperties("livekitbin_srctrack", map[string]interface{}{
		"track": glib.ArbitraryValue{Data: track},
		"pub":   glib.ArbitraryValue{Data: pub},
		"rp":    glib.ArbitraryValue{Data: rp},
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error creating source track element for track ID %s: %v", track.ID(), err))
		self.Error(fmt.Sprintf("Error creating source track element for track ID %s", track.ID()), err)
		return
	}
	element.GetStaticPad("src").AddProbe(gst.PadProbeTypeEventDownstream, livekittracks.PadProbeDropEOS)
	element.GetStaticPad("src_rtcp").AddProbe(gst.PadProbeTypeEventDownstream, livekittracks.PadProbeDropEOS)

	if err := self.Add(element); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error adding source track element to bin for track ID %s: %v", track.ID(), err))
		self.Error(fmt.Sprintf("Error adding source track element to bin for track ID %s", track.ID()), err)
		return
	}

	if ret := element.GetStaticPad("src").Link(funnel.RtpFunnel.GetRequestPad(fmt.Sprintf("sink_%d", track.SSRC()))); ret != gst.PadLinkOK {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error linking source pad to sink funnel for track %s: %v", track.ID(), ret))
		self.Error(fmt.Sprintf("Error linking source pad to sink funnel for track %s", track.ID()), fmt.Errorf("link error: %v", ret))
		return
	}
	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Linked source pad to sink funnel for track ID: %s", track.ID()))

	if ret := element.GetStaticPad("src_rtcp").Link(funnel.RtcpFunnel.GetRequestPad(fmt.Sprintf("sink_%d", track.SSRC()))); ret != gst.PadLinkOK {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error linking RTCP source pad to sink funnel for track %s: %v", track.ID(), ret))
		self.Error(fmt.Sprintf("Error linking RTCP source pad to sink funnel for track %s", track.ID()), fmt.Errorf("link error: %v", ret))
		return
	}

	if !element.SyncStateWithParent() {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error syncing state with parent for track %s: %v", track.ID(), err))
		self.Error(fmt.Sprintf("Error syncing state with parent for track %s", track.ID()), fmt.Errorf("sync error"))
		return
	}

	srcTrack = &LivekitBinTrack{
		Track:    track,
		Pub:      pub,
		Rp:       rp,
		TrackSrc: element,
	}
	e.tracks[pub.SID()] = srcTrack
	e.sidBySsrc[uint32(track.SSRC())] = pub.SID()

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Linked RTCP source pad to sink funnel for track ID: %s", track.ID()))
}

func (e *LivekitBin) onRtpBinPadAddedRecvRtp(self *gst.Bin, pad *gst.Pad, pname string) {
	var session, ssrc, pt int
	_, err := fmt.Sscanf(pname, "recv_rtp_src_%d_%d_%d", &session, &ssrc, &pt)
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error parsing pad name %s: %v", pname, err))
		return
	}

	switch livekit.TrackSource(session) {
	case livekit.TrackSource_MICROPHONE,
		livekit.TrackSource_CAMERA,
		livekit.TrackSource_SCREEN_SHARE,
		livekit.TrackSource_SCREEN_SHARE_AUDIO:
	default:
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Unknown track source in pad name: %s", pname))
		return
	}

	sid, exists := e.sidBySsrc[uint32(ssrc)]
	if !exists {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to find track SID for pad name: %s", pname))
		self.Error(fmt.Sprintf("Failed to find track SID for pad name: %s", pname), fmt.Errorf("track error"))
		return
	}
	track, ok := e.tracks[sid]
	if !ok {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to find track for SID %s in pad name: %s", sid, pname))
		self.Error(fmt.Sprintf("Failed to find track for SID %s in pad name: %s", sid, pname), fmt.Errorf("track error"))
		return
	}

	class := gst.ToElementClass(self.Class())

	gpname := fmt.Sprintf("recv_rtp_src_%d_%d_%d", session, ssrc, pt)
	gpad := gst.NewGhostPadFromTemplate(gpname, pad, class.GetPadTemplate("recv_rtp_src_%u_%u_%u"))
	if gpad == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error creating ghost pad for pad name %s", pname))
		return
	}

	sourceInfo := livekittracks.NewTrackSourceInfo(track.Rp, track.Pub)
	pad.PushEvent(gst.NewCustomEvent(gst.EventTypeCustomDownstreamSticky, sourceInfo.Structure().Transfer()))

	if !self.AddPad(gpad.Pad) {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error adding ghost pad for pad name %s", pname))
		return
	}
	if !gpad.SetActive(true) {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error activating ghost pad for pad name %s", pname))
		return
	}

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("new track with pad name: %s", pname))
}

func (e *LivekitBin) UnsubscribeTrack(track *webrtc.TrackRemote, pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
	self := gst.ToGstBin(e.self.Get())
	if self == nil || self.Instance() == nil {
		return
	}

	kind := pub.Source()
	var funnel *LivekitBinTrackFunnel
	switch kind {
	case livekit.TrackSource_CAMERA:
		funnel = &e.funnels[kind]
	case livekit.TrackSource_MICROPHONE:
		funnel = &e.funnels[kind]
	case livekit.TrackSource_SCREEN_SHARE:
		funnel = &e.funnels[kind]
	case livekit.TrackSource_SCREEN_SHARE_AUDIO:
		funnel = &e.funnels[kind]
	default:
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Unknown track source for track %s: %d", track.ID(), pub.Source()))
		return
	}

	ssrc := track.SSRC()
	sid := pub.SID()

	e.mu.Lock()
	defer e.mu.Unlock()

	srcTrack, ok := e.tracks[sid]
	if !ok {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Track with SID %s not found during unsubscribe for track ID %s", sid, track.ID()))
		return
	}

	if err := srcTrack.TrackSrc.SetState(gst.StateNull); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error setting track source element to NULL for track ID %s: %v", track.ID(), err))
	}

	if err := self.Remove(srcTrack.TrackSrc); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error removing track source element from bin for track ID %s: %v", track.ID(), err))
	}

	if pad := funnel.RtpFunnel.GetStaticPad(fmt.Sprintf("sink_%d", ssrc)); pad != nil {
		funnel.RtpFunnel.ReleaseRequestPad(pad)
	}
	if pad := funnel.RtcpFunnel.GetStaticPad(fmt.Sprintf("sink_%d", ssrc)); pad != nil {
		funnel.RtcpFunnel.ReleaseRequestPad(pad)
	}

	delete(e.tracks, sid)
	delete(e.sidBySsrc, uint32(ssrc))

	if _, err := e.RtpBin.Emit("clear-ssrc", uint(kind), uint(ssrc)); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error emitting pad removed signal for track ID %s: %v", track.ID(), err))
		self.Error(fmt.Sprintf("Error emitting pad removed signal for track ID %s", track.ID()), err)
	}

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Unsubscribed and removed track with ID: %s", track.ID()))
}

func (e *LivekitBin) onRtpBinPadRemovedRecvRtp(self *gst.Bin, pad *gst.Pad, pname string) {
	var session, ssrc, pt int
	_, err := fmt.Sscanf(pname, "recv_rtp_src_%d_%d_%d", &session, &ssrc, &pt)
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error parsing pad name %s: %v", pname, err))
		return
	}

	switch livekit.TrackSource(session) {
	case livekit.TrackSource_MICROPHONE,
		livekit.TrackSource_CAMERA,
		livekit.TrackSource_SCREEN_SHARE,
		livekit.TrackSource_SCREEN_SHARE_AUDIO:
	default:
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Unknown track source in pad name: %s", pname))
		return
	}

	gpname := fmt.Sprintf("recv_rtp_src_%d_%d_%d", session, ssrc, pt)
	gpad := self.GetStaticPad(gpname)
	if gpad == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to find ghost pad for pad name: %s", pname))
		return
	}
	if !self.RemovePad(gpad) {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error removing ghost pad for pad name %s", pname))
		return
	}
}

func (e *LivekitBin) onRtpBinPadRemovedSendRtp(self *gst.Bin, pad *gst.Pad, pname string) {
	var session int
	_, err := fmt.Sscanf(pname, "send_rtp_src_%d", &session)
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error parsing pad name %s: %v", pname, err))
		return
	}

	kind := livekit.TrackSource(session)
	switch kind {
	case livekit.TrackSource_MICROPHONE,
		livekit.TrackSource_CAMERA,
		livekit.TrackSource_SCREEN_SHARE,
		livekit.TrackSource_SCREEN_SHARE_AUDIO:
	default:
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Unknown track source in pad name: %s", pname))
		return
	}

	pub := e.publications[kind]
	if pub == nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Publication not found for track source %s when removing pad name %s", kind.String(), pname))
		return
	}

	if !pub.initialized {
		return
	}

	if err := pub.TrackQueue.SetState(gst.StateNull); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error setting track queue element to NULL for track source %s when removing pad name %s: %v", kind.String(), pname, err))
	}
	if err := pub.TrackSink.SetState(gst.StateNull); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error setting track sink element to NULL for track source %s when removing pad name %s: %v", kind.String(), pname, err))
	}

	if err := self.RemoveMany(pub.TrackQueue, pub.TrackSink); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error removing track sink element from bin for track source %s when removing pad name %s: %v", kind.String(), pname, err))
	}

	pub.initialized = false
}

func (e *LivekitBin) releasePadSendRtpSink(self *gst.Bin, pad *gst.Pad) {
	gpad := pad.AsGhostPad()
	if gpad == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Released pad %s is not a ghost pad", pad.GetName()))
		return
	}

	var session int
	_, err := fmt.Sscanf(gpad.GetName(), "send_rtp_sink_%d", &session)
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error parsing ghost pad name %s: %v", gpad.GetName(), err))
		return
	}

	kind := livekit.TrackSource(session)
	switch kind {
	case livekit.TrackSource_MICROPHONE,
		livekit.TrackSource_CAMERA,
		livekit.TrackSource_SCREEN_SHARE,
		livekit.TrackSource_SCREEN_SHARE_AUDIO:
	default:
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Unknown track source in ghost pad name: %s", gpad.GetName()))
		return
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	if !self.RemovePad(gpad.Pad) {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to remove ghost pad %s for released pad %s", gpad.GetName(), pad.GetName()))
		return
	}

	pub := e.publications[kind]
	if pub == nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Publication not found for track source %s when releasing pad %s", kind.String(), gpad.GetName()))
		return
	}

	if err := pub.FormatFilter.SetState(gst.StateNull); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error setting format filter to NULL for track source %s when releasing pad %s: %v", kind.String(), gpad.GetName(), err))
	}

	if err := self.Remove(pub.FormatFilter); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Error removing format filter from bin for track source %s when releasing pad %s: %v", kind.String(), gpad.GetName(), err))
	}

	if pad := e.RtpBin.GetStaticPad(fmt.Sprintf("send_rtp_sink_%d", session)); pad != nil {
		e.RtpBin.ReleaseRequestPad(pad)
	}

	// sid := ""
	// if e.room.LocalParticipant != nil {
	// 	tp := e.room.LocalParticipant.GetTrackPublication(kind)
	// 	if tp != nil {
	// 		sid = tp.SID()
	// 	}
	// }
	// if sid != "" {
	// 	if err := e.room.LocalParticipant.UnpublishTrack(sid); err != nil {
	// 		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to unpublish track for track source %s when releasing pad %s: %v", kind.String(), gpad.GetName(), err))
	// 	}
	// } else {
	// 	self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Publication SID is empty for track source %s when releasing pad %s, skipping unpublish: sid=%q", kind.String(), gpad.GetName(), sid))
	// }

	e.publications[kind] = nil

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Released and cleaned up resources for track source %s after pad %s was released", kind.String(), gpad.GetName()))
}
