package livekittracks

import (
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/base"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4"
)

var srcTrackRtpProperties = []*glib.ParamSpec{
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

type SrcTrackRtp struct {
	Track   *webrtc.TrackRemote
	Pub     *lksdk.RemoteTrackPublication
	Rp      *lksdk.RemoteParticipant
	unblock atomic.Bool
}

func (*SrcTrackRtp) New() glib.GoObjectSubclass {
	return &SrcTrackRtp{}
}

func (*SrcTrackRtp) ClassInit(klass *glib.ObjectClass) {
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"lkroom_srctrack_rtp",
		"src",
		"Receives RTP packets from a WebRTC PeerConnection",
		"Roomkit <roomkit-visio@numerique.gouv.fr>",
	)

	CAT.Log(gst.LevelDebug, "Adding pad template")
	class.AddPadTemplate(gst.NewPadTemplate(
		"src",
		gst.PadDirectionSource,
		gst.PadPresenceAlways,
		gst.NewCapsFromString("application/x-rtp")))

	class.InstallProperties(srcTrackRtpProperties)
}

func (s *SrcTrackRtp) InstanceInit(instance *glib.Object) {
	self := base.ToGstBaseSrc(instance)

	self.SetLive(true)
	self.SetFormat(gst.FormatTime)
	self.SetAsync(false)
	self.SetDoTimestamp(true)
}

func (s *SrcTrackRtp) SetProperty(instance *glib.Object, id uint, value *glib.Value) {
	self := base.ToGstBaseSrc(instance)
	param := srcTrackRtpProperties[id]
	switch param.Name() {
	case "track":
		gv, err := value.GoValue()
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get Go value for track property: %v", err))
			self.Error("Failed to get Go value for track property", err)
			return
		}
		if gv == nil {
			return
		}
		data, ok := gv.(glib.ArbitraryValue)
		if !ok {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Invalid type for track property: %T", gv))
			self.Error("Invalid type for track property", fmt.Errorf("expected glib.ArbitraryValue, got %T", gv))
			return
		}
		track, ok := data.Data.(*webrtc.TrackRemote)
		if !ok {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Invalid data type for track property: %T", data.Data))
			self.Error("Invalid data type for track property", fmt.Errorf("expected *webrtc.TrackRemote, got %T", data.Data))
			return
		}
		s.Track = track
	case "pub":
		gv, err := value.GoValue()
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get Go value for pub property: %v", err))
			self.Error("Failed to get Go value for pub property", err)
			return
		}
		if gv == nil {
			return
		}
		data, ok := gv.(glib.ArbitraryValue)
		if !ok {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Invalid type for pub property: %T", gv))
			self.Error("Invalid type for pub property", fmt.Errorf("expected glib.ArbitraryValue, got %T", gv))
			return
		}
		pub, ok := data.Data.(*lksdk.RemoteTrackPublication)
		if !ok {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Invalid data type for pub property: %T", data.Data))
			self.Error("Invalid data type for pub property", fmt.Errorf("expected *lksdk.RemoteTrackPublication, got %T", data.Data))
			return
		}
		s.Pub = pub
	case "rp":
		gv, err := value.GoValue()
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get Go value for rp property: %v", err))
			self.Error("Failed to get Go value for rp property", err)
			return
		}
		if gv == nil {
			return
		}
		data, ok := gv.(glib.ArbitraryValue)
		if !ok {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Invalid type for rp property: %T", gv))
			self.Error("Invalid type for rp property", fmt.Errorf("expected glib.ArbitraryValue, got %T", gv))
			return
		}
		rp, ok := data.Data.(*lksdk.RemoteParticipant)
		if !ok {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Invalid data type for rp property: %T", data.Data))
			self.Error("Invalid data type for rp property", fmt.Errorf("expected *lksdk.RemoteParticipant, got %T", data.Data))
			return
		}
		s.Rp = rp
	default:
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Unknown property ID: %d", id))
		self.Error(fmt.Sprintf("Unknown property ID: %d", id), nil)
	}
}

func (s *SrcTrackRtp) Constructed(instance *glib.Object) {
	self := base.ToGstBaseSrc(instance)
	self.Log(CAT, gst.LevelDebug, "Constructed")

	if s.Track == nil || s.Pub == nil || s.Rp == nil {
		err := errors.New("Track, Pub, and Rp must be set before constructing SrcTrackRtp")
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to construct SrcTrackRtp: %v", err))
		self.Error("Failed to construct SrcTrackRtp: Track, Pub, and Rp must be set", err)
		return
	}

	s.unblock.Store(false)

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Constructed SrcTrackRtp for track %s from participant %s", s.Track.ID(), s.Rp.Identity()))
}

func (s *SrcTrackRtp) SetCaps(self *base.GstBaseSrc, caps *gst.Caps) bool {
	return true
}

func (s *SrcTrackRtp) GetCaps(self *base.GstBaseSrc, filter *gst.Caps) *gst.Caps {
	var mediaType string
	switch s.Pub.Source() {
	case livekit.TrackSource_CAMERA, livekit.TrackSource_SCREEN_SHARE:
		mediaType = "video"
	case livekit.TrackSource_MICROPHONE, livekit.TrackSource_SCREEN_SHARE_AUDIO:
		mediaType = "audio"
	default:
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Unsupported track source: %s", s.Pub.Source()))
		self.Error(fmt.Sprintf("Unsupported track source: %s", s.Pub.Source()), nil)
		return gst.NewEmptyCaps()
	}
	caps := gst.NewCapsFromString(fmt.Sprintf("application/x-rtp, media=(string)%s, rtcp-fb-nack-pli=(boolean)true, rtcp-fb-ccm-fir=(boolean)true", mediaType))
	if filter != nil && filter.Instance() != nil && !filter.IsEmpty() && !filter.IsAny() {
		if intersect := caps.Intersect(filter); intersect != nil {
			return intersect
		}
	}
	return caps.Copy().Ref()
}

func (s *SrcTrackRtp) Start(self *base.GstBaseSrc) bool {
	self.Log(CAT, gst.LevelDebug, "Starting")

	s.unblock.Store(false)
	if err := s.Track.SetReadDeadline(time.Time{}); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to reset read deadline on track: %v", err))
		self.Error("Failed to reset read deadline on track", err)
		return false
	}

	return true
}

func (s *SrcTrackRtp) Stop(self *base.GstBaseSrc) bool {
	self.Log(CAT, gst.LevelDebug, "Stopping")
	if err := s.Pub.SetSubscribed(false); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to unsubscribe from track publication: %v", err))
	}

	return true
}

func (s *SrcTrackRtp) Fill(self *base.GstBaseSrc, offset uint64, length uint, buffer *gst.Buffer) gst.FlowReturn {
	if s.unblock.Load() {
		self.Log(CAT, gst.LevelInfo, "Fill called but unblock is set, returning EOS")
		return gst.FlowFlushing
	}

	mapInfo := buffer.Map(gst.MapWrite)
	defer buffer.Unmap()

	ptr := mapInfo.Data()
	data := unsafe.Slice((*byte)(ptr), length)

	n, _, err := s.Track.Read(data)
	if s.unblock.Load() {
		self.Log(CAT, gst.LevelInfo, "Fill unblocked, returning Flushing")
		return gst.FlowFlushing
	}
	if err != nil {
		if err == io.EOF {
			return gst.FlowEOS
		}
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to read from io.Reader: %T: %v", err, err))
		self.Error("Failed to read from io.Reader", err)
		return gst.FlowError
	}

	if uint(n) < length {
		buffer.SetSize(int64(n))
	}
	return gst.FlowOK
}

func (s *SrcTrackRtp) Unlock(self *base.GstBaseSrc) bool {
	self.Log(CAT, gst.LevelInfo, "SrcTrackRtp Unlock called, unblocking Fill and sending EOS")

	s.unblock.Store(true)

	if err := s.Track.SetReadDeadline(time.Now()); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to set read deadline on track: %v", err))
		self.Error("Failed to set read deadline on track", err)
		return false
	}

	return true
}

func (s *SrcTrackRtp) UnlockStop(self *base.GstBaseSrc) bool {
	self.Log(CAT, gst.LevelInfo, "SrcTrackRtp UnlockStop called")
	s.unblock.Store(false)
	if err := s.Track.SetReadDeadline(time.Time{}); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to reset read deadline on track: %v", err))
		self.Error("Failed to reset read deadline on track", err)
		return false
	}
	return true
}

func (s *SrcTrackRtp) Finalize(instance *glib.Object) {
	s.Track = nil
	s.Pub = nil
	s.Rp = nil
}
