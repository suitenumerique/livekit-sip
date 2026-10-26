package livekittracks

import (
	"errors"
	"fmt"
	"sync/atomic"
	"time"

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
	glib.NewBoxedParam(
		"track-factory",
		"Track Factory",
		"Creates the primary and backup webrtc tracks for a new publication",
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

// TrackFactory creates the primary and the optional backup track of a publication.
type TrackFactory func() (*lksdk.LocalTrack, *lksdk.LocalTrack, error)

type SinkTrack struct {
	track       atomic.Pointer[lksdk.LocalTrack]
	backupTrack atomic.Pointer[lksdk.LocalTrack]
	factory     TrackFactory
	stale       atomic.Bool
	pub         atomic.Pointer[lksdk.LocalTrackPublication]
	lp          *lksdk.LocalParticipant
	opts        *lksdk.TrackPublicationOptions
	useBackup   atomic.Bool
	publishing  atomic.Bool
	off         atomic.Bool
	stopped     atomic.Bool

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
}

func (s *SinkTrack) Constructed(instance *glib.Object) {
	self := base.ToGstBaseSink(instance)
	if s.track.Load() == nil || s.lp == nil {
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
	if s.track.Load() == nil {
		self.Log(CAT, gst.LevelError, "Track is not set in sink_track")
		self.Error("Track is not set", errors.New("track is nil"))
		return false
	}

	s.stopped.Store(false)
	s.publish(self)

	return true
}

func (s *SinkTrack) Stop(self *base.GstBaseSink) bool {
	self.Log(CAT, gst.LevelDebug, "Stopping")
	s.stopped.Store(true)
	s.unPublish(self)
	return true
}

func (s *SinkTrack) Render(self *base.GstBaseSink, buffer *gst.Buffer) gst.FlowReturn {
	track := s.track.Load()
	if track == nil {
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
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to unmarshal RTP packet\nerr=%v", err))
		self.Error("Failed to unmarshal RTP packet", err)
		return gst.FlowError
	}

	if s.useBackup.Load() {
		if backup := s.backupTrack.Load(); backup != nil {
			track = backup
		}
	}

	if err := track.WriteRTP(&s.rtp, nil); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to write RTCP packet to track\nerr=%v", err))
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

	s.stale.Store(true)
	if err := s.lp.UnpublishTrack(pub.SID()); err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to unpublish track\nerr=%v", err))
	}
}

// renewTracks replaces the tracks with new ones, which carry new track ids.
func (s *SinkTrack) renewTracks() (oldID, newID string, err error) {
	if s.factory == nil {
		return "", "", errors.New("no track factory")
	}
	track, backup, err := s.factory()
	if err != nil {
		return "", "", err
	}
	if track == nil {
		return "", "", errors.New("track factory returned no track")
	}
	if old := s.track.Swap(track); old != nil {
		oldID = old.ID()
	}
	s.backupTrack.Store(backup)
	s.stale.Store(false)
	return oldID, track.ID(), nil
}

func (s *SinkTrack) renewTracksLogged(self *base.GstBaseSink, reason string) bool {
	oldID, newID, err := s.renewTracks()
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create new tracks\nreason=%s\nerr=%v", reason, err))
		return false
	}
	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Renewed tracks before publishing\nreason=%s\nold_track=%s\nnew_track=%s", reason, oldID, newID))
	return true
}

// publishAsync publishes the track from a goroutine.
func (s *SinkTrack) publishAsync(self *base.GstBaseSink) {
	wself := glib.WeakRefInit(self)
	go func() {
		obj := wself.Get()
		if obj == nil {
			return
		}
		s.publish(base.ToGstBaseSink(obj))
	}()
}

// publish publishes the track unless a publish is in flight, and unpublishes
// it again when the track was stopped or turned off meanwhile.
func (s *SinkTrack) publish(self *base.GstBaseSink) {
	if s.pub.Load() != nil {
		self.Log(CAT, gst.LevelWarning, "Track is already published, skipping publish")
		return
	}
	if !s.publishing.CompareAndSwap(false, true) {
		self.Log(CAT, gst.LevelDebug, "Track publish already in flight, skipping publish")
		return
	}
	defer s.publishing.Store(false)

	if s.stale.Load() && s.factory != nil {
		s.renewTracksLogged(self, "republish")
	}
	pub, err := s.publishTracks()
	if errors.Is(err, lksdk.ErrTrackPublishTimeout) && !s.stopped.Load() && !s.off.Load() && s.factory != nil && s.renewTracksLogged(self, "publish timeout") {
		self.Log(CAT, gst.LevelWarning, "Track publish timed out, retrying with new tracks")
		pub, err = s.publishTracks()
	}
	if err != nil {
		if s.stopped.Load() || s.off.Load() {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to publish track no longer wanted\nerr=%v", err))
			return
		}
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to publish track\nerr=%v", err))
		self.Error("Failed to publish track", err)
		return
	}
	s.pub.Store(pub)
	if s.stopped.Load() || s.off.Load() {
		self.Log(CAT, gst.LevelInfo, "Unpublishing track turned off while publishing")
		s.unPublish(self)
		return
	}

	// Warn when neither the primary nor the backup track binds within 10s.
	track := s.track.Load()
	backupTrack := s.backupTrack.Load()
	name := s.opts.Name
	time.AfterFunc(10*time.Second, func() {
		if s.pub.Load() == nil {
			return
		}
		bound := track != nil && track.IsBound()
		if !bound && backupTrack != nil {
			bound = backupTrack.IsBound()
		}
		if !bound {
			CAT.Log(gst.LevelWarning, fmt.Sprintf("Published track never bound; codec likely rejected by the SFU\ntrack=%s", name))
		}
	})
}

func (s *SinkTrack) Event(self *base.GstBaseSink, event *gst.Event) bool {
	switch event.Type() {
	case gst.EventTypeCustomOOB:
		structure := event.GetStructure()
		switch structure.Name() {
		case sipbin.EventOOBStreamOff:
			self.Log(CAT, gst.LevelDebug, "Received OOB Stream Off event, stopping track")
			s.off.Store(true)
			s.unPublish(self)
			return true
		case sipbin.EventOOBStreamOn:
			self.Log(CAT, gst.LevelDebug, "Received OOB Stream On event, starting track")
			s.off.Store(false)
			s.publishAsync(self)
			return true
		}
	}

	return self.ParentEvent(event)
}

func (s *SinkTrack) publishTracks() (*lksdk.LocalTrackPublication, error) {
	var pubOpts []lksdk.LocalTrackPublishOption
	if backup := s.backupTrack.Load(); backup != nil {
		pubOpts = append(pubOpts, lksdk.WithBackupCodec(backup))
	}
	return s.lp.PublishTrack(s.track.Load(), s.opts, pubOpts...)
}

func (s *SinkTrack) Finalize(instance *glib.Object) {
	s.track.Store(nil)
	s.backupTrack.Store(nil)
	s.factory = nil
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
		track, ok := data.Data.(*lksdk.LocalTrack)
		if !ok {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Invalid data type for track property\ntype=%T", data.Data))
			self.Error("Invalid data type for track property", fmt.Errorf("expected *lksdk.LocalTrack, got %T", data.Data))
			return
		}
		s.track.Store(track)
	case "backupTrack":
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
		track, ok := data.Data.(*lksdk.LocalTrack)
		if !ok {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Invalid data type for track property\ntype=%T", data.Data))
			self.Error("Invalid data type for track property", fmt.Errorf("expected *lksdk.LocalTrack, got %T", data.Data))
			return
		}
		s.backupTrack.Store(track)
	case "track-factory":
		gv, err := value.GoValue()
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get Go value for track-factory property\nerr=%v", err))
			return
		}
		data, ok := gv.(glib.ArbitraryValue)
		if !ok || data.Data == nil {
			return
		}
		factory, ok := data.Data.(TrackFactory)
		if !ok {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Invalid data type for track-factory property\ntype=%T", data.Data))
			return
		}
		s.factory = factory
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
		pub, ok := data.Data.(*lksdk.LocalTrackPublication)
		if !ok {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Invalid data type for pub property\ntype=%T", data.Data))
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
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get Go value for lp property\nerr=%v", err))
			self.Error("Failed to get Go value for lp property", err)
			return
		}
		if gv == nil {
			return
		}
		data, ok := gv.(glib.ArbitraryValue)
		if !ok {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Invalid type for lp property\ntype=%T", gv))
			self.Error("Invalid type for lp property", fmt.Errorf("expected glib.ArbitraryValue, got %T", gv))
			return
		}
		lp, ok := data.Data.(*lksdk.LocalParticipant)
		if !ok {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Invalid data type for lp property\ntype=%T", data.Data))
			self.Error("Invalid data type for lp property", fmt.Errorf("expected *lksdk.LocalParticipant, got %T", data.Data))
			return
		}
		s.lp = lp
	case "opts":
		gv, err := value.GoValue()
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get Go value for opts property\nerr=%v", err))
			self.Error("Failed to get Go value for opts property", err)
			return
		}
		if gv == nil {
			return
		}
		data, ok := gv.(glib.ArbitraryValue)
		if !ok {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Invalid type for opts property\ntype=%T", gv))
			self.Error("Invalid type for opts property", fmt.Errorf("expected glib.ArbitraryValue, got %T", gv))
			return
		}
		opts, ok := data.Data.(*lksdk.TrackPublicationOptions)
		if !ok {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Invalid data type for opts property\ntype=%T", data.Data))
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
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get Go value for useBackupTrack property\nerr=%v", err))
			self.Error("Failed to get Go value for useBackupTrack property", err)
			return
		}
		useBackup, ok := gv.(bool)
		if !ok {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Invalid type for useBackupTrack property\ntype=%T", gv))
			self.Error("Invalid type for useBackupTrack property", fmt.Errorf("expected bool, got %T", gv))
			return
		}
		s.useBackup.Store(useBackup)
	default:
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Unknown property ID for SinkTrack\nid=%d", id))
		self.Error(fmt.Sprintf("Unknown property ID %d for SinkTrack", id), nil)
	}
}
