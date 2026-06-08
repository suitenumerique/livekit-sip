package livekitbin

import (
	"fmt"
	"sync"
	"weak"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/livekitbin/livekittracks"
	"github.com/pion/webrtc/v4"
)

var CAT = gst.NewDebugCategory(
	"livekitbin",
	gst.DebugColorFgGreen,
	"livekitbin Element",
)

func init() {
	livekittracks.CAT = CAT
}

const MAX_ACTIVE_PARTICIPANTS = 100
const NbTracks = int(livekit.TrackSource_SCREEN_SHARE_AUDIO) + 1

type config struct {
	wsURL                        string
	token                        string
	defaultParticipantIdentity   string
	defaultParticipantName       string
	defaultParticipantAttributes map[string]string
	maxActiveParticipants        uint
	microphone                   bool
	microphoneMimeType           string
	camera                       bool
	cameraMimeType               string
	screenshare                  bool
	screenshareMimeType          string
	screenshareAudio             bool
	screenshareAudioMimeType     string
}

type LivekitBinTrack struct {
	TrackSrc *gst.Element
	Track    *webrtc.TrackRemote
	Pub      *lksdk.RemoteTrackPublication
	Rp       *lksdk.RemoteParticipant
}

type LivekitBinPublication struct {
	initialized  bool
	probeID      uint64
	TrackSink    *gst.Element
	FormatFilter *gst.Element
	Track        *webrtc.TrackLocalStaticRTP
}

type LivekitBinTrackFunnel struct {
	initialized bool
	RtpFunnel   *gst.Element
	RtcpFunnel  *gst.Element
}

type LivekitBinRtcp struct {
	initialized bool
	sessions    [NbTracks]bool
	SinkRtcp    *gst.Element
	RtcpFunnel  *gst.Element
}

type LivekitBin struct {
	mu     sync.Mutex
	self   *glib.WeakRef
	RtpBin *gst.Element

	state
	config
	room *lksdk.Room

	PtMap [NbTracks]map[uint8]*gst.Caps // indexed by livekit.TrackSource
	ptMu  sync.RWMutex

	rtcp         LivekitBinRtcp
	funnels      [NbTracks]LivekitBinTrackFunnel // indexed by livekit.TrackSource
	tracks       map[string]*LivekitBinTrack     // key is track SID
	sidBySsrc    map[uint32]string               // maps track SSRC to track SID
	publications [NbTracks]*LivekitBinPublication

	activeSpeakers []string

	livekitMu sync.Mutex
}

func (e *LivekitBin) New() glib.GoObjectSubclass {
	return &LivekitBin{}
}

// ClassInit implements [glib.GoObjectSubclass].
func (e *LivekitBin) ClassInit(klass *glib.ObjectClass) {
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"LiveKit Room",
		"Source/Sink",
		"Element to connect to a LiveKit room",
		"Roomkit <roomkit-visio@numerique.gouv.fr>",
	)

	// signals
	gst.SignalNew(
		class.Type(),
		"closed",
		gst.SignalRunLast,
		glib.TYPE_NONE,
	)

	gst.SignalNew(
		class.Type(),
		"connected",
		gst.SignalRunLast,
		glib.TYPE_NONE,
	)

	gst.SignalNew(
		class.Type(),
		"active-speakers-changed",
		gst.SignalRunLast,
		glib.TYPE_NONE,
		gst.TypeStructure, // TrackSourceInfo
	)

	gst.SignalNew(
		class.Type(),
		"participant-join",
		gst.SignalRunLast,
		glib.TYPE_NONE,
		gst.TypeStructure, // ParticipantInfo
	)

	gst.SignalNew(
		class.Type(),
		"participant-left",
		gst.SignalRunLast,
		glib.TYPE_NONE,
		gst.TypeStructure, // ParticipantInfo
	)

	// action signals
	gst.SignalNew(
		class.Type(),
		"connect",
		gst.SignalRunLast,
		glib.TYPE_NONE,
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

func (e *LivekitBin) InstanceInit(instance *glib.Object) {
	self := gst.ToGstBin(instance)

	e.state.cond = sync.NewCond(&e.state.mu)
	e.defaultParticipantAttributes = make(map[string]string)
	for i := range e.PtMap {
		e.PtMap[i] = make(map[uint8]*gst.Caps)
	}
	e.self = glib.WeakRefInit(self)
	e.config.maxActiveParticipants = 6
	e.config.microphoneMimeType = webrtc.MimeTypeOpus
	e.config.cameraMimeType = webrtc.MimeTypeVP8
	e.config.screenshareMimeType = webrtc.MimeTypeVP8
	e.config.screenshareAudioMimeType = webrtc.MimeTypeOpus
	e.tracks = make(map[string]*LivekitBinTrack)
	e.sidBySsrc = make(map[uint32]string)
}

func (e *LivekitBin) Constructed(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	eweak := weak.Make(e)

	var err error
	e.RtpBin, err = gst.NewElementWithProperties("rtpbin", map[string]interface{}{
		"rtp-profile":              int(3), // GST_RTP_PROFILE_AVPF
		"autoremove":               true,
		"max-misorder-time":        uint(200),
		"max-dropout-time":         uint(200),
		"max-ts-offset":            int(200000000),
		"timeout-inactive-sources": true,
		"drop-on-latency":          false,
		"latency":                  uint(200),
		"do-lost":                  true,
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create rtpbin: %v", err))
		self.Error("Failed to create rtpbin", err)
		return
	}
	e.setupRtpBinSignals(self)

	if err := self.AddMany(e.RtpBin); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add children to livekitbin: %v", err))
		self.Error("Failed to add children to livekitbin", err)
		return
	}

	e.room = lksdk.NewRoom(e.callabcks())

	// action signals
	if _, err := self.Connect("connect", func(instance *gst.Element) {
		ptr := eweak.Value()
		if ptr == nil {
			CAT.Log(gst.LevelError, "LivekitBin instance is nil in connect signal callback")
			return
		}
		go ptr.OnConnectSignal(instance)
	}); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to connect to connect signal: %v", err))
		self.Error("Failed to connect to connect signal", err)
		return
	}
}

func (e *LivekitBin) ChangeState(instance *gst.Element, transition gst.StateChange) gst.StateChangeReturn {
	self := gst.ToGstBin(instance)

	self.Log(CAT, gst.LevelDebug, fmt.Sprintf("LivekitBin state change: %s", transition.String()))
	defer self.Log(CAT, gst.LevelDebug, fmt.Sprintf("LivekitBin state change completed: %s", transition.String()))

	if transition == gst.StateChangeReadyToNull {
		e.Close()
	}

	ret := self.ParentChangeState(transition)

	return ret
}

func (e *LivekitBin) RequestNewPad(instance *gst.Element, templ *gst.PadTemplate, name string, caps *gst.Caps) *gst.Pad {
	self := gst.ToGstBin(instance)

	switch templ.Name() {
	case "send_rtp_sink_%u":
		return e.requestNewPadSendRtp(instance, templ, name, caps)
	}

	self.Log(CAT, gst.LevelError, fmt.Sprintf("Unknown pad template name: %s", templ.Name()))
	return nil
}

func (e *LivekitBin) ReleasePad(instance *gst.Element, pad *gst.Pad) {
	self := gst.ToGstBin(instance)

	templ := pad.Template()
	if templ == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Pad %s has no template", pad.GetName()))
		return
	}

	switch templ.Name() {
	case "send_rtp_sink_%u":
		e.releasePadSendRtpSink(self, pad)
	default:
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Unknown pad template for released pad %s: %s", pad.GetName(), templ.Name()))
	}
}

func (e *LivekitBin) Finalize(instance *glib.Object) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.ptMu.Lock()
	defer e.ptMu.Unlock()
	e.livekitMu.Lock()
	defer e.livekitMu.Unlock()

	e.RtpBin = nil
	e.tracks = nil
	e.sidBySsrc = nil
	e.publications = [NbTracks]*LivekitBinPublication{}
	e.rtcp = LivekitBinRtcp{}
	e.funnels = [NbTracks]LivekitBinTrackFunnel{}
	e.room = nil
	e.PtMap = [NbTracks]map[uint8]*gst.Caps{}
}
