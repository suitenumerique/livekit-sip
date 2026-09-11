package livekitbin

import (
	"fmt"
	"sync"
	"time"
	"weak"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/livekitbin/livekittracks"
	"github.com/livekit/sip/pkg/sip/pipeline/metrics"
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

var AudioCodecMimeTypes = []string{
	webrtc.MimeTypeOpus,
	webrtc.MimeTypePCMU,
	webrtc.MimeTypePCMA,
}

var VideoCodecMimeTypes = []string{
	webrtc.MimeTypeH264,
	webrtc.MimeTypeVP8,
	webrtc.MimeTypeVP9,
}

type config struct {
	wsURL                        string
	token                        string
	defaultParticipantIdentity   string
	defaultParticipantName       string
	defaultParticipantAttributes map[string]string
	maxActiveParticipants        uint
	maxAudioParticipants         uint
	audioJitter                  uint
	videoJitter                  uint
	microphone                   bool
	microphoneMimeType           string
	camera                       bool
	cameraMimeType               string
	screenshare                  bool
	screenshareMimeType          string
	screenshareAudio             bool
	screenshareAudioMimeType     string
	videoWidth                   uint
	videoHeight                  uint
	screenshareWidth             uint
	screenshareHeight            uint
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
	TrackQueue   *gst.Element
	TrackSink    *gst.Element
	FormatFilter *gst.Element
	Track        *webrtc.TrackLocalStaticRTP

	keyframeMu      sync.Mutex
	lastKeyframeReq time.Time
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
	trackMu      sync.RWMutex                    // guards tracks and sidBySsrc, which rtpbin streaming threads read
	tracks       map[string]*LivekitBinTrack     // key is track SID
	sidBySsrc    map[uint32]string               // maps track SSRC to track SID
	publications [NbTracks]*LivekitBinPublication

	activeSpeakers []string

	partMu    sync.Mutex
	announced map[string]struct{}  // participants introduced to the compositor (participant-join sent)
	seenAt    map[string]time.Time // first sighting, for a stable mosaic fill order

	audioMu         sync.Mutex
	audioLastActive map[string]time.Time

	cameraMu   sync.Mutex
	cameraDims map[string][2]uint32 // key is track SID

	idleMu    sync.Mutex
	idle      map[string]*idleSubscription // key is track SID
	idleGrace time.Duration                // 0 = subscriptionIdleGrace

	subMu  sync.Mutex
	wanted map[string]struct{} // track SIDs requested from the SFU and not released yet

	jbMu          sync.Mutex
	jitterbuffers map[uint64]*gst.Element // key is session<<32 | ssrc
	jbStatsTimer  bool

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
	e.audioLastActive = make(map[string]time.Time)
	e.announced = make(map[string]struct{})
	e.seenAt = make(map[string]time.Time)
	e.cameraDims = make(map[string][2]uint32)
	e.wanted = make(map[string]struct{})
	e.jitterbuffers = make(map[uint64]*gst.Element)
	for i := range e.PtMap {
		e.PtMap[i] = make(map[uint8]*gst.Caps)
	}
	e.self = glib.WeakRefInit(self)
	e.config.maxActiveParticipants = 6
	e.config.audioJitter = 80
	e.config.videoJitter = 200
	e.config.microphoneMimeType = webrtc.MimeTypeOpus
	e.config.cameraMimeType = webrtc.MimeTypeVP8
	e.config.screenshareMimeType = webrtc.MimeTypeVP8
	e.config.screenshareAudioMimeType = webrtc.MimeTypeOpus
	e.config.videoWidth = 1280
	e.config.videoHeight = 720
	e.config.screenshareWidth = 1920
	e.config.screenshareHeight = 1080
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
		"max-ts-offset":            int(200000000),
		"timeout-inactive-sources": true,
		"drop-on-latency":          false,
		"latency":                  uint(200),
		"do-lost":                  true,
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create rtpbin\nerr=%v", err))
		self.Error("Failed to create rtpbin", err)
		return
	}
	e.setupRtpBinSignals(self)

	if err := self.AddMany(e.RtpBin); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add children to livekitbin\nerr=%v", err))
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
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to connect to connect signal\nerr=%v", err))
		self.Error("Failed to connect to connect signal", err)
		return
	}
}

func (e *LivekitBin) ChangeState(instance *gst.Element, transition gst.StateChange) gst.StateChangeReturn {
	self := gst.ToGstBin(instance)

	self.Log(CAT, gst.LevelDebug, fmt.Sprintf("LivekitBin state change\ntransition=%s", transition.String()))
	defer self.Log(CAT, gst.LevelDebug, fmt.Sprintf("LivekitBin state change completed\ntransition=%s", transition.String()))

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

	self.Log(CAT, gst.LevelError, fmt.Sprintf("Unknown pad template name\nname=%s", templ.Name()))
	return nil
}

func (e *LivekitBin) ReleasePad(instance *gst.Element, pad *gst.Pad) {
	self := gst.ToGstBin(instance)

	templ := pad.Template()
	if templ == nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Pad has no template\nname=%s", pad.GetName()))
		return
	}

	switch templ.Name() {
	case "send_rtp_sink_%u":
		e.releasePadSendRtpSink(self, pad)
	default:
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Unknown pad template for released pad\nname=%s\ntemplate=%s", pad.GetName(), templ.Name()))
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
	e.trackMu.Lock()
	for _, t := range e.tracks {
		metrics.TrackSubscribed(trackSourceLabel(t), -1)
	}
	e.tracks = nil
	e.sidBySsrc = nil
	e.trackMu.Unlock()
	e.publications = [NbTracks]*LivekitBinPublication{}
	e.rtcp = LivekitBinRtcp{}
	e.funnels = [NbTracks]LivekitBinTrackFunnel{}
	e.room = nil
	e.PtMap = [NbTracks]map[uint8]*gst.Caps{}
}
