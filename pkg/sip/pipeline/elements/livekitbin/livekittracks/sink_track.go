package livekittracks

import (
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/base"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/sipbin"
	"github.com/pion/rtp"
)

var sinkTrackProperties = []*glib.ParamSpec{
	glib.NewBoxedParam(
		"track",
		"Track",
		"The webrtc track this element will write to",
		glib.TYPE_ARBITRARY_DATA,
		glib.ParameterWritable|glib.ParameterConstructOnly,
	),
	glib.NewBoxedParam(
		"backupTrack",
		"Backup Track",
		"The backup webrtc track for this element",
		glib.TYPE_ARBITRARY_DATA,
		glib.ParameterWritable|glib.ParameterConstructOnly,
	),
	glib.NewBoxedParam(
		"pub",
		"Pub",
		"The livekit publication for this track",
		glib.TYPE_ARBITRARY_DATA,
		glib.ParameterWritable,
	),
	glib.NewBoxedParam(
		"lp",
		"LP",
		"The livekit participant for this track",
		glib.TYPE_ARBITRARY_DATA,
		glib.ParameterWritable|glib.ParameterConstructOnly,
	),
	glib.NewBoxedParam(
		"opts",
		"Options",
		"Track publication options",
		glib.TYPE_ARBITRARY_DATA,
		glib.ParameterWritable|glib.ParameterConstructOnly,
	),
	glib.NewBoolParam(
		"use-backup",
		"Use Backup Track",
		"Whether to use the backup track for this element",
		false,
		glib.ParameterWritable,
	),
}

type SinkTrack struct {
	track       *lksdk.LocalTrack
	backupTrack *lksdk.LocalTrack
	pub         atomic.Pointer[lksdk.LocalTrackPublication]
	lp          *lksdk.LocalParticipant
	opts        *lksdk.TrackPublicationOptions
	useBackup   atomic.Bool

	rtp rtp.Packet
}

func (*SinkTrack) New() glib.GoObjectSubclass {
	return &SinkTrack{}
}

func (*SinkTrack) ClassInit(klass *glib.ObjectClass) {
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"sink_track",
		"sink",
		"Sends packets to a WebRTC PeerConnection",
		"Roomkit <roomkit-visio@numerique.gouv.fr>",
	)

	CAT.Log(gst.LevelDebug, "Adding pad template")
	class.AddPadTemplate(gst.NewPadTemplate(
		"sink",
		gst.PadDirectionSink,
		gst.PadPresenceAlways,
		gst.NewCapsFromString("application/x-rtp")))

	class.InstallProperties(sinkTrackProperties)
}

func (s *SinkTrack) InstanceInit(instance *glib.Object) {
	self := base.ToGstBaseSink(instance)

	self.SetSync(false)
	self.SetAsyncEnabled(false)
	self.SetMaxBitrate(1_500_000)
}

func (s *SinkTrack) Constructed(instance *glib.Object) {
	self := base.ToGstBaseSink(instance)
	if s.track == nil || s.lp == nil {
		self.Log(CAT, gst.LevelError, "Track, publication, or participant is not set in sink_track")
		self.Error("Track, publication, or participant is not set", errors.New("one or more required fields are nil"))
		return
	}
}

func (s *SinkTrack) SetCaps(self *base.GstBaseSink, caps *gst.Caps) bool {
	return true
}

func (s *SinkTrack) GetCaps(self *base.GstBaseSink, filter *gst.Caps) *gst.Caps {
	caps := gst.NewCapsFromString("application/x-rtp")
	if filter != nil && filter.Instance() != nil && !filter.IsEmpty() && !filter.IsAny() {
		if intersect := caps.Intersect(filter); intersect != nil {
			return intersect
		}
	}
	return caps.Copy().Ref()
}

func (s *SinkTrack) Start(self *base.GstBaseSink) bool {
	if s.track == nil {
		self.Log(CAT, gst.LevelError, "Track is not set in sink_track")
		self.Error("Track is not set", errors.New("track is nil"))
		return false
	}

	s.publish(self)

	return true
}

func (s *SinkTrack) Stop(self *base.GstBaseSink) bool {
	self.Log(CAT, gst.LevelDebug, "Stopping")
	s.unPublish(self)
	return true
}

func (s *SinkTrack) Render(self *base.GstBaseSink, buffer *gst.Buffer) gst.FlowReturn {
	if s.track == nil {
		self.Log(CAT, gst.LevelError, "Track is not set in sink_track")
		self.Error("Track is not set", errors.New("track is nil"))
		return gst.FlowError
	}

	// TODO: do we need to prevent writes when muted?
	// if yes then we will need to send a force key unit event upstream
	// pub := s.pub
	// if pub == nil || pub.IsMuted() {
	// 	return gst.FlowOK
	// }

	if err := s.rtp.Unmarshal(buffer.Bytes()); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to unmarshal RTP packet: %v", err))
		self.Error("Failed to unmarshal RTP packet", err)
		return gst.FlowError
	}

	var track *lksdk.LocalTrack
	if s.useBackup.Load() && s.backupTrack != nil {
		track = s.backupTrack
	} else {
		track = s.track
	}

	if err := track.WriteRTP(&s.rtp, nil); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to write RTCP packet to track: %v", err))
		self.Error("Failed to write RTCP packet to track", err)
		return gst.FlowError
	}

	return gst.FlowOK
}

func (s *SinkTrack) unPublish(self *base.GstBaseSink) {
	pub := s.pub.Swap(nil)
	if pub == nil {
		return
	}

	tp := s.lp.GetTrackPublication(pub.Source())
	if tp == nil {
		return
	}

	if err := s.lp.UnpublishTrack(tp.SID()); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to unpublish track: %v", err))
	}
}

func (s *SinkTrack) publish(self *base.GstBaseSink) {
	if s.pub.Load() != nil {
		self.Log(CAT, gst.LevelWarning, "Track is already published, skipping publish")
		return
	}
	var pubOpts []lksdk.LocalTrackPublishOption
	if s.backupTrack != nil {
		pubOpts = append(pubOpts, lksdk.WithBackupCodec(s.backupTrack))
	}
	pub, err := s.lp.PublishTrack(s.track, s.opts, pubOpts...)
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to publish track: %v", err))
		self.Error("Failed to publish track", err)
		return
	}
	s.pub.Store(pub)
}

func (s *SinkTrack) Event(self *base.GstBaseSink, event *gst.Event) bool {
	switch event.Type() {
	case gst.EventTypeCustomOOB:
		structure := event.GetStructure()
		switch structure.Name() {
		case sipbin.EventOOBStreamOff:
			self.Log(CAT, gst.LevelInfo, "Received OOB Stream Off event, stopping track")
			s.unPublish(self)
			return true
		case sipbin.EventOOBStreamOn:
			self.Log(CAT, gst.LevelInfo, "Received OOB Stream On event, starting track")
			s.publish(self)
			return true
		}
	}

	return self.ParentEvent(event)
}

func (s *SinkTrack) Finalize(instance *glib.Object) {
	s.track = nil
	s.pub.Store(nil)
	s.lp = nil
}

func (s *SinkTrack) SetProperty(instance *glib.Object, id uint, value *glib.Value) {
	self := base.ToGstBaseSink(instance)
	param := sinkTrackProperties[id]
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
		track, ok := data.Data.(*lksdk.LocalTrack)
		if !ok {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Invalid data type for track property: %T", data.Data))
			self.Error("Invalid data type for track property", fmt.Errorf("expected *lksdk.LocalTrack, got %T", data.Data))
			return
		}
		s.track = track
	case "backupTrack":
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
		track, ok := data.Data.(*lksdk.LocalTrack)
		if !ok {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Invalid data type for track property: %T", data.Data))
			self.Error("Invalid data type for track property", fmt.Errorf("expected *lksdk.LocalTrack, got %T", data.Data))
			return
		}
		s.backupTrack = track
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
		pub, ok := data.Data.(*lksdk.LocalTrackPublication)
		if !ok {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Invalid data type for pub property: %T", data.Data))
			self.Error("Invalid data type for pub property", fmt.Errorf("expected *lksdk.LocalTrackPublication, got %T", data.Data))
			return
		}
		if pub == nil {
			return
		}
		s.pub.Store(pub)
	case "lp":
		gv, err := value.GoValue()
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get Go value for lp property: %v", err))
			self.Error("Failed to get Go value for lp property", err)
			return
		}
		if gv == nil {
			return
		}
		data, ok := gv.(glib.ArbitraryValue)
		if !ok {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Invalid type for lp property: %T", gv))
			self.Error("Invalid type for lp property", fmt.Errorf("expected glib.ArbitraryValue, got %T", gv))
			return
		}
		lp, ok := data.Data.(*lksdk.LocalParticipant)
		if !ok {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Invalid data type for lp property: %T", data.Data))
			self.Error("Invalid data type for lp property", fmt.Errorf("expected *lksdk.LocalParticipant, got %T", data.Data))
			return
		}
		s.lp = lp
	case "opts":
		gv, err := value.GoValue()
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get Go value for opts property: %v", err))
			self.Error("Failed to get Go value for opts property", err)
			return
		}
		if gv == nil {
			return
		}
		data, ok := gv.(glib.ArbitraryValue)
		if !ok {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Invalid type for opts property: %T", gv))
			self.Error("Invalid type for opts property", fmt.Errorf("expected glib.ArbitraryValue, got %T", gv))
			return
		}
		opts, ok := data.Data.(*lksdk.TrackPublicationOptions)
		if !ok {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Invalid data type for opts property: %T", data.Data))
			self.Error("Invalid data type for opts property", fmt.Errorf("expected *lksdk.TrackPublicationOptions, got %T", data.Data))
			return
		}
		if opts == nil {
			return
		}
		s.opts = opts
	case "use-backup":
		gv, err := value.GoValue()
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get Go value for useBackupTrack property: %v", err))
			self.Error("Failed to get Go value for useBackupTrack property", err)
			return
		}
		useBackup, ok := gv.(bool)
		if !ok {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Invalid type for useBackupTrack property: %T", gv))
			self.Error("Invalid type for useBackupTrack property", fmt.Errorf("expected bool, got %T", gv))
			return
		}
		s.useBackup.Store(useBackup)
	default:
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Unknown property ID %d for SinkTrack", id))
		self.Error(fmt.Sprintf("Unknown property ID %d for SinkTrack", id), nil)
	}
}
