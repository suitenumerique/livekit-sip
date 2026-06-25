package livekittracks

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
)

const (
	QDataSrcTrackSource = "livekitbin_srctrack-element-source"
	SrcTrackNamePrefix  = "livekitbin_srctrack_"
)

var srcTrackProperties = []*glib.ParamSpec{
	glib.NewBoolParam(
		"enabled",
		"Enabled",
		"Whether the track is enabled",
		false,
		glib.ParameterReadable|glib.ParameterWritable,
	),
	glib.NewBoxedParam(
		"track",
		"Track",
		"The webrtc track this element will read from",
		glib.TYPE_ARBITRARY_DATA,
		glib.ParameterWritable|glib.ParameterConstructOnly,
	),
	glib.NewBoxedParam(
		"pub",
		"Publication",
		"The LiveKit publication for the track this element will read from",
		glib.TYPE_ARBITRARY_DATA,
		glib.ParameterWritable|glib.ParameterConstructOnly,
	),
	glib.NewBoxedParam(
		"rp",
		"RemoteParticipant",
		"The LiveKit RemoteParticipant that published the track this element will read from",
		glib.TYPE_ARBITRARY_DATA,
		glib.ParameterWritable|glib.ParameterConstructOnly,
	),
}

func SrcTrackName(sid string) string {
	return SrcTrackNamePrefix + sid
}

type SrcTrack struct {
	Track *webrtc.TrackRemote
	Pub   *lksdk.RemoteTrackPublication
	Rp    *lksdk.RemoteParticipant

	src *gst.Element

	keyframeMu      sync.Mutex
	lastKeyframeReq time.Time
}

func (*SrcTrack) New() glib.GoObjectSubclass {
	return &SrcTrack{}
}

func (*SrcTrack) ClassInit(klass *glib.ObjectClass) {
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"livekitbin_srctrack",
		"src",
		"Receives packets from a WebRTC PeerConnection",
		"Roomkit <roomkit-visio@numerique.gouv.fr>",
	)

	gst.SignalNew(
		class.Type(),
		"send-info",
		gst.SignalRunLast,
		glib.TYPE_NONE,
	)

	class.AddPadTemplate(gst.NewPadTemplate(
		"src",
		gst.PadDirectionSource,
		gst.PadPresenceAlways,
		gst.NewAnyCaps()))

	class.AddPadTemplate(gst.NewPadTemplate(
		"src_rtcp",
		gst.PadDirectionSource,
		gst.PadPresenceAlways,
		gst.NewCapsFromString("application/x-rtcp")))

	class.InstallProperties(srcTrackProperties)
}

func (s *SrcTrack) Constructed(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	class := gst.ToElementClass(self.Class())

	if s.Track == nil || s.Pub == nil || s.Rp == nil {
		self.Log(CAT, gst.LevelError, "Track, Pub, and Rp properties must be set before constructing SrcTrack element")
		self.Error("Track, Pub, and Rp properties must be set before constructing SrcTrack element", errors.New("missing required properties"))
		return
	}

	var err error
	s.src, err = gst.NewElementWithProperties("livekitbin_srctrack_rtp", map[string]interface{}{
		"track": glib.ArbitraryValue{Data: s.Track},
		"pub":   glib.ArbitraryValue{Data: s.Pub},
		"rp":    glib.ArbitraryValue{Data: s.Rp},
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create srctrack_rtp\nerr=%v", err))
		self.Error("Failed to create srctrack_rtp", err)
		return
	}

	if err := self.Add(s.src); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add srctrack_rtp\nerr=%v", err))
		self.Error("Failed to add srctrack_rtp", err)
		return
	}

	gsrcPad := gst.NewGhostPadFromTemplate("src", s.src.GetStaticPad("src"), class.GetPadTemplate("src"))
	self.AddPad(gsrcPad.Pad)

	gsrcPad.Pad.AddProbe(gst.PadProbeTypeEventUpstream, func(_ *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		if ev := info.GetEvent(); ev != nil && ev.HasName("GstForceKeyUnit") {
			s.requestKeyframe()
		}
		return gst.PadProbeOK
	})

	// rtcp
	rtcpPad := gst.NewPadFromTemplate(class.GetPadTemplate("src_rtcp"), "src_rtcp")
	rtcpPad.UseFixedCaps()
	self.AddPad(rtcpPad)
}

func (s *SrcTrack) requestKeyframe() {
	if s.Rp == nil || s.Track == nil {
		return
	}

	s.keyframeMu.Lock()
	now := time.Now()
	if !s.lastKeyframeReq.IsZero() && now.Sub(s.lastKeyframeReq) < time.Second {
		s.keyframeMu.Unlock()
		return
	}
	s.lastKeyframeReq = now
	s.keyframeMu.Unlock()

	s.Rp.WritePLI(s.Track.SSRC())
	CAT.Log(gst.LevelDebug, fmt.Sprintf("Requested keyframe (PLI) from participant\nparticipant=%s\nssrc=%d", s.Rp.Identity(), s.Track.SSRC()))
}

func (s *SrcTrack) open(self *gst.Bin) gst.StateChangeReturn {
	self.Log(CAT, gst.LevelDebug, "Opening SrcTrack element")

	rtcpPad := self.GetStaticPad("src_rtcp")

	if !rtcpPad.SetActive(true) {
		self.Log(CAT, gst.LevelError, "Failed to activate src_rtcp pad for srcTrack element")
		self.Error("Failed to activate src_rtcp pad for srcTrack element", errors.New("failed to activate src_rtcp pad"))
		return gst.StateChangeFailure
	}

	streamID := rtcpPad.CreateStreamID(self.Element, "rtcp")
	self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Created RTCP stream ID\nstream_id=%s", streamID))
	evt := gst.NewStreamStartEvent(streamID)
	evt.SetGroupID(gst.NextGroupID())
	if !rtcpPad.PushEvent(evt) && !rtcpPad.IsLinked() {
		self.Log(CAT, gst.LevelError, "Failed to push StreamStart event on src_rtcp pad")
		self.Error("Failed to push StreamStart event on src_rtcp pad", errors.New("push event failed"))
		return gst.StateChangeFailure
	}

	caps := gst.NewCapsFromString("application/x-rtcp")
	if !rtcpPad.PushEvent(gst.NewCapsEvent(caps.Copy())) {
		self.Log(CAT, gst.LevelWarning, "Failed to push caps event on rtcp pad")
		if rtcpPad.IsLinked() {
			self.Log(CAT, gst.LevelWarning, "Failed to push Caps event on RTCP pad")
		}
	}

	segment := gst.NewFormattedSegment(gst.FormatTime)
	if !rtcpPad.PushEvent(gst.NewSegmentEvent(segment)) {
		if rtcpPad.IsLinked() {
			self.Log(CAT, gst.LevelWarning, "Failed to push Segment event on RTCP pad")
		}
	}

	return gst.StateChangeSuccess
}

func (s *SrcTrack) start(self *gst.Bin) gst.StateChangeReturn {
	self.Log(CAT, gst.LevelDebug, "Starting SrcTrack element")

	rtcpPad := self.GetStaticPad("src_rtcp")
	s.Pub.OnRTCP(s.onRtcp(self, rtcpPad))

	return gst.StateChangeSuccess
}

func (s *SrcTrack) stop(self *gst.Bin) gst.StateChangeReturn {
	self.Log(CAT, gst.LevelDebug, "Stopping SrcTrack element")

	s.Pub.OnRTCP(nil)

	done := make(chan struct{})
	go func() {
		s.SendRtcpBye(self)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(1 * time.Second):
		self.Log(CAT, gst.LevelWarning, "Timeout waiting for SendRtcpBye to complete")
	}

	self.Log(CAT, gst.LevelDebug, "Stopped SrcTrack element and sent RTCP BYE")

	return gst.StateChangeSuccess
}

func (s *SrcTrack) SendRtcpBye(self *gst.Bin) {
	rtcpPad := self.GetStaticPad("src_rtcp")
	if rtcpPad == nil {
		self.Log(CAT, gst.LevelWarning, "Failed to get src_rtcp pad while sending RTCP BYE in SrcTrack element")
		return
	}
	s.pushRtcp(self, rtcpPad, &rtcp.Goodbye{
		Sources: []uint32{uint32(s.Track.SSRC())},
	})
}

func (s *SrcTrack) ChangeState(instance *gst.Element, transition gst.StateChange) gst.StateChangeReturn {
	self := gst.ToGstBin(instance)
	self.Log(CAT, gst.LevelDebug, fmt.Sprintf("SrcTrack state change\nsid=%s\ntransition=%s", s.Pub.SID(), transition.String()))
	defer self.Log(CAT, gst.LevelDebug, fmt.Sprintf("SrcTrack state change completed\nsid=%s\ntransition=%s", s.Pub.SID(), transition.String()))

	switch transition {
	case gst.StateChangeNullToReady:
		if ret := s.open(self); ret != gst.StateChangeSuccess {
			return ret
		}
	case gst.StateChangePausedToPlaying:
		if ret := s.start(self); ret != gst.StateChangeSuccess {
			return ret
		}
	case gst.StateChangePlayingToPaused:
		s.stop(self)
	}

	return self.ParentChangeState(transition)
}

func (s *SrcTrack) Finalize(instance *glib.Object) {
	s.Track = nil
	s.Pub = nil
	s.Rp = nil
	s.src = nil
}

func filterSSRC(pkt rtcp.Packet, ssrc uint32) rtcp.Packet {
	switch p := pkt.(type) {
	case *rtcp.SenderReport:
		if p.SSRC != ssrc {
			return nil
		}
		return pkt
	case *rtcp.ReceiverReport:
		if p.SSRC != ssrc {
			return nil
		}
		return pkt
	case *rtcp.Goodbye:
		res := &rtcp.Goodbye{
			Sources: []uint32{},
			Reason:  p.Reason,
		}
		for _, s := range p.Sources {
			if s == ssrc {
				res.Sources = append(res.Sources, s)
			}
		}
		if len(res.Sources) == 0 {
			return nil
		}
		return res
	case *rtcp.SourceDescription:
		res := &rtcp.SourceDescription{
			Chunks: []rtcp.SourceDescriptionChunk{},
		}
		for _, c := range p.Chunks {
			if c.Source == ssrc {
				res.Chunks = append(res.Chunks, c)
			}
		}
		if len(res.Chunks) == 0 {
			return nil
		}
		return res
	case *rtcp.PictureLossIndication:
		if p.SenderSSRC != ssrc {
			return nil
		}
		return p
	case *rtcp.FullIntraRequest:
		if p.SenderSSRC != ssrc {
			return nil
		}
		return p
	case *rtcp.ExtendedReport:
		if p.SenderSSRC != ssrc {
			return nil
		}
		return p
	}
	return nil
}

func (s *SrcTrack) pushRtcp(self *gst.Bin, rtcpPad *gst.Pad, pkt rtcp.Packet) {
	raw, err := pkt.Marshal()
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to marshal RTCP packet\nerr=%v", err))
		self.Error("Failed to marshal RTCP packet", err)
		return
	}

	buf := gst.NewBufferFromBytes(raw)
	if ret := rtcpPad.Push(buf); ret != gst.FlowOK {
		if ret == gst.FlowNotLinked {
			self.Log(CAT, gst.LevelDebug, "RTCP pad is not linked, dropping RTCP packet")
			return
		}
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to push RTCP buffer\nret=%v", ret))
	}
}

func (s *SrcTrack) onRtcp(self *gst.Bin, rtcpPad *gst.Pad) func(p rtcp.Packet) {
	return func(p rtcp.Packet) {
		switch p.(type) {
		case *rtcp.Goodbye:
			self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Sending RTCP BYE\nsource=%s\nssrc=%d\nparticipant=%s", s.Pub.Source(), s.Track.SSRC(), s.Rp.Identity()))
		case *rtcp.PictureLossIndication, *rtcp.FullIntraRequest:
			self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Sending RTCP PLI/FIR\nsource=%s\nssrc=%d\nparticipant=%s", s.Pub.Source(), s.Track.SSRC(), s.Rp.Identity()))
		default:
			self.Log(CAT, gst.LevelTrace, fmt.Sprintf("Pushing RTCP packet\ntype=%T\nvalue=%+v", p, p))
		}

		if _, ok := p.(*rtcp.Goodbye); ok {
			return
		}

		filtered := filterSSRC(p, uint32(s.Track.SSRC()))
		if filtered == nil {
			return
		}

		self.Log(CAT, gst.LevelTrace, fmt.Sprintf("Pushing RTCP packet\ntype=%T\nvalue=%+v", filtered, filtered))

		s.pushRtcp(self, rtcpPad, filtered)
	}
}

func (s *SrcTrack) GetProperty(instance *glib.Object, id uint) *glib.Value {
	self := gst.ToGstBin(instance)
	param := srcTrackProperties[id]
	switch param.Name() {
	case "enabled":
		enabled := s.Pub.IsEnabled()
		val, err := glib.GValue(enabled)
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get enabled property value\nerr=%v", err))
			return nil
		}
		return val
	default:
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Unknown property\nname=%s", param.Name()))
		return nil
	}
}

func (s *SrcTrack) SetProperty(instance *glib.Object, id uint, value *glib.Value) {
	self := gst.ToGstBin(instance)
	param := srcTrackProperties[id]
	switch param.Name() {
	case "enabled":
		enabledVal, err := value.GoValue()
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get bool value for enabled property\nerr=%v", err))
			return
		}
		enabled, ok := enabledVal.(bool)
		if !ok {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to convert enabled property value to bool\nvalue=%v", enabledVal))
			return
		}
		s.Pub.SetEnabled(enabled)
	case "track":
		gv, err := value.GoValue()
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get Go value for track property\nerr=%v", err))
			self.Error("Failed to get Go value for track property", err)
			return
		}
		if gv == nil {
			return
		}
		data, ok := gv.(glib.ArbitraryValue)
		if !ok {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Invalid type for track property\ntype=%T", gv))
			self.Error("Invalid type for track property", fmt.Errorf("expected glib.ArbitraryValue, got %T", gv))
			return
		}
		track, ok := data.Data.(*webrtc.TrackRemote)
		if !ok {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Invalid data type for track property\ntype=%T", data.Data))
			self.Error("Invalid data type for track property", fmt.Errorf("expected *webrtc.TrackRemote, got %T", data.Data))
			return
		}
		s.Track = track
	case "pub":
		gv, err := value.GoValue()
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get Go value for pub property\nerr=%v", err))
			self.Error("Failed to get Go value for pub property", err)
			return
		}
		if gv == nil {
			return
		}
		data, ok := gv.(glib.ArbitraryValue)
		if !ok {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Invalid type for pub property\ntype=%T", gv))
			self.Error("Invalid type for pub property", fmt.Errorf("expected glib.ArbitraryValue, got %T", gv))
			return
		}
		pub, ok := data.Data.(*lksdk.RemoteTrackPublication)
		if !ok {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Invalid data type for pub property\ntype=%T", data.Data))
			self.Error("Invalid data type for pub property", fmt.Errorf("expected *lksdk.RemoteTrackPublication, got %T", data.Data))
			return
		}
		s.Pub = pub
	case "rp":
		gv, err := value.GoValue()
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get Go value for rp property\nerr=%v", err))
			self.Error("Failed to get Go value for rp property", err)
			return
		}
		if gv == nil {
			return
		}
		data, ok := gv.(glib.ArbitraryValue)
		if !ok {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Invalid type for rp property\ntype=%T", gv))
			self.Error("Invalid type for rp property", fmt.Errorf("expected glib.ArbitraryValue, got %T", gv))
			return
		}
		rp, ok := data.Data.(*lksdk.RemoteParticipant)
		if !ok {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Invalid data type for rp property\ntype=%T", data.Data))
			self.Error("Invalid data type for rp property", fmt.Errorf("expected *lksdk.RemoteParticipant, got %T", data.Data))
			return
		}
		s.Rp = rp
	default:
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Unknown property\nname=%s", param.Name()))
	}
}
