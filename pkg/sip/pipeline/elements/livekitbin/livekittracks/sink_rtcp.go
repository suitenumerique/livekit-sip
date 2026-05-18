package livekittracks

import (
	"errors"
	"fmt"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/base"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
)

var sinkRtcpProperties = []*glib.ParamSpec{
	glib.NewBoxedParam(
		"pc",
		"PeerConnection",
		"The WebRTC PeerConnection to send RTCP packets to",
		glib.TYPE_ARBITRARY_DATA,
		glib.ParameterWritable|glib.ParameterConstructOnly,
	),
}

type SinkRtcp struct {
	pc *webrtc.PeerConnection
}

func (*SinkRtcp) New() glib.GoObjectSubclass {
	return &SinkRtcp{}
}

func (*SinkRtcp) ClassInit(klass *glib.ObjectClass) {
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"sink_rtcp",
		"sink/rtcp",
		"Sends RTCP packets to a WebRTC PeerConnection",
		"Roomkit <roomkit-visio@numerique.gouv.fr>",
	)

	CAT.Log(gst.LevelDebug, "Adding pad template")
	class.AddPadTemplate(gst.NewPadTemplate(
		"sink",
		gst.PadDirectionSink,
		gst.PadPresenceAlways,
		gst.NewCapsFromString("application/x-rtcp")))
	
	class.InstallProperties(sinkRtcpProperties)
}

func (s *SinkRtcp) InstanceInit(instance *glib.Object) {
	self := base.ToGstBaseSink(instance)

	self.SetSync(false)
	self.SetAsyncEnabled(false)
	self.SetMaxBitrate(500_000)
}

func (s *SinkRtcp) Constructed(instance *glib.Object) {
	self := base.ToGstBaseSink(instance)

	if s.pc == nil {
		self.Log(CAT, gst.LevelError, "PeerConnection is not set in sink_rtcp")
		self.Error("PeerConnection is not set", errors.New("peerconnection is nil"))
		return
	}
}

func (s *SinkRtcp) SetCaps(self *base.GstBaseSink, caps *gst.Caps) bool {
	return true
}

func (s *SinkRtcp) GetCaps(self *base.GstBaseSink, filter *gst.Caps) *gst.Caps {
	caps := gst.NewCapsFromString("application/x-rtcp")
	if filter != nil && filter.Instance() != nil && !filter.IsEmpty() && !filter.IsAny() {
		if intersect := caps.Intersect(filter); intersect != nil {
			return intersect
		}
	}
	return caps.Copy().Ref()
}

func (s *SinkRtcp) Start(self *base.GstBaseSink) bool {
	return true
}

func (s *SinkRtcp) Stop(self *base.GstBaseSink) bool {
	self.Log(CAT, gst.LevelDebug, "Stopping")

	s.pc = nil

	return true
}

func (s *SinkRtcp) Render(self *base.GstBaseSink, buffer *gst.Buffer) gst.FlowReturn {
	if state := s.pc.ConnectionState(); state != webrtc.PeerConnectionStateConnected {
		self.Log(CAT, gst.LevelTrace, fmt.Sprintf("PeerConnection is not connected (state: %s), dropping RTCP packet", state.String()))
		return gst.FlowOK
	}

	pkts, err := rtcp.Unmarshal(buffer.Bytes())
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to unmarshal RTCP packet: %v", err))
		self.Error("Failed to unmarshal RTCP packet", err)
		return gst.FlowError
	}

	self.Log(CAT, gst.LevelTrace, fmt.Sprintf("Sending %d RTCP packets to PeerConnection: %+v", len(pkts), pkts))

	if err := s.pc.WriteRTCP(pkts); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to write RTCP packets to PeerConnection: %v", err))
		self.Error("Failed to write RTCP packets to PeerConnection", err)
		return gst.FlowError
	}

	return gst.FlowOK
}

func (s *SinkRtcp) Finalize(instance *glib.Object) {
	s.pc = nil
}

func (s *SinkRtcp) SetProperty(instance *glib.Object, id uint, value *glib.Value) {
	self := base.ToGstBaseSink(instance)
	param := sinkRtcpProperties[id]
	switch param.Name() {
	case "pc":
		gv, err := value.GoValue()
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get Go value for property 'pc': %v", err))
			self.Error("Failed to get Go value for property 'pc'", err)
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
		pc, ok := data.Data.(*webrtc.PeerConnection)
		if !ok {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Invalid data type for property 'pc': %T", data.Data))
			self.Error("Invalid data type for property 'pc'", fmt.Errorf("expected *webrtc.PeerConnection, got %T", data.Data))
			return
		}
		s.pc = pc
	default:
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Unknown property ID %d", id))
		self.Error("Unknown property ID", fmt.Errorf("unknown property ID %d", id))
	}
}
