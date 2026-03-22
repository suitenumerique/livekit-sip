package sip

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	sdpv2 "github.com/livekit/media-sdk/sdp/v2"
	"github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/livekit/sip/pkg/sip/pipeline"
)

var (
	ErrWrongState = errors.New("media orchestrator in wrong state")
)

const (
	ScreenshareMSTreamID = 2
)

type AudioInfo interface {
	Port() uint16
	Codec() *sdpv2.Codec
	AvailableCodecs() []*sdpv2.Codec
	SetMedia(media *sdpv2.SDPMedia)
	SetDst(addr netip.AddrPort)
}

type dispatchOperation struct {
	fn   func() error
	done chan error
}

type MediaState int

const (
	MediaStateFailed MediaState = iota - 1
	MediaStateNew
	MediaStateOK
	MediaStateReady
	MediaStateStarted
	MediaStateStopped
)

func (ms MediaState) String() string {
	switch ms {
	case MediaStateFailed:
		return "failed"
	case MediaStateNew:
		return "new"
	case MediaStateReady:
		return "ready"
	case MediaStateStarted:
		return "started"
	case MediaStateStopped:
		return "stopped"
	default:
		return "unknown"
	}
}

type MediaOrchestrator struct {
	ctx     context.Context
	cancel  context.CancelFunc
	log     logger.Logger
	opts    *MediaOptions
	inbound *sipInbound

	dispatchCH chan dispatchOperation
	cleanupCH  chan func()
	dispatchOK atomic.Bool
	wg         sync.WaitGroup

	audioinfo   AudioInfo
	camera      *CameraManager
	screenshare *ScreenshareManager
	tracks      *TrackManager
	bfcp        *BFCPManager

	sdp             *sdpv2.SDP
	delayedOfferSDP *sdpv2.SDP // SDP offer sent in 200 OK for delayed offer calls
	state           MediaState

	// Active speaker debounce: store latest speakers under mutex, single dispatch on timer fire
	pendingSpeakers    []lksdk.Participant
	pendingSpeakersMu  sync.Mutex
	activeSpeakerTimer *time.Timer

	room *Room
}

func NewMediaOrchestrator(log logger.Logger, ctx context.Context, inbound *sipInbound, room *Room, audioinfo AudioInfo, opts *MediaOptions) (*MediaOrchestrator, error) {
	ctx, cancel := context.WithCancel(ctx)
	o := &MediaOrchestrator{
		ctx:        ctx,
		cancel:     cancel,
		log:        log,
		opts:       opts,
		inbound:    inbound,
		dispatchCH: make(chan dispatchOperation, 1),
		cleanupCH:  make(chan func(), 16),
		audioinfo:  audioinfo,
		state:      MediaStateNew,
	}

	o.wg.Add(1)
	go o.dispatchLoop()
	for !o.dispatchOK.Load() {
		runtime.Gosched() // wait for dispatch loop to start
	}

	if err := o.dispatch(func() error {
		return o.init(room)
	}); err != nil {
		return nil, err
	}

	return o, nil
}

func (o *MediaOrchestrator) init(room *Room) error {
	if err := o.okStates(MediaStateNew); err != nil {
		return err
	}

	o.room = room
	o.tracks = NewTrackManager(o.log.WithComponent("track_manager"))

	camera, err := NewCameraManager(o.log.WithComponent("camera"), o.ctx, room, o.opts, o.tracks)
	if err != nil {
		return fmt.Errorf("could not create video manager: %w", err)
	}
	o.camera = camera

	screenshare, err := NewScreenshareManager(o.log.WithComponent("screenshare"), o.ctx, o.opts)
	if err != nil {
		return fmt.Errorf("could not create screenshare manager: %w", err)
	}
	o.screenshare = screenshare

	// BFCP manager is created lazily in setupBFCP() once we know the transport from SDP

	// Wire up screenshare lifecycle to BFCP floor control
	o.screenshare.OnScreenshareStarted = func() {
		if o.bfcp != nil {
			o.log.Infow("screenshare started, requesting BFCP floor for virtual client")
			if err := o.bfcp.RequestFloorForVirtualClient(); err != nil {
				o.log.Warnw("failed to request BFCP floor for screenshare", err)
			}
		}
	}
	o.screenshare.OnScreenshareStopped = func() {
		if o.bfcp != nil {
			o.log.Infow("screenshare stopped, releasing BFCP floor for virtual client")
			if err := o.bfcp.ReleaseFloorForVirtualClient(); err != nil {
				o.log.Warnw("failed to release BFCP floor for screenshare", err)
			}
		}
	}

	o.state = MediaStateOK

	return nil
}

func (o *MediaOrchestrator) okStates(allowed ...MediaState) error {
	for _, state := range allowed {
		if o.state == state {
			return nil
		}
	}
	return fmt.Errorf("invalid state: %s, expected one of %v: %w", o.state, allowed, ErrWrongState)
}

const DispatchTimeout = 20 * time.Second

func (o *MediaOrchestrator) dispatch(fn func() error) error {
	if !o.dispatchOK.Load() {
		return ErrWrongState
	}

	done := make(chan error)
	op := dispatchOperation{
		fn:   fn,
		done: done,
	}

	timeout := time.After(DispatchTimeout)

	select {
	case o.dispatchCH <- op:
		break
	case <-o.ctx.Done():
		return context.Canceled
	case <-timeout:
		o.log.Errorw("media orchestrator dispatch operation timed out", nil, "timeout", DispatchTimeout)
		return fmt.Errorf("media orchestrator dispatch operation timed out after %v: %w", DispatchTimeout, context.DeadlineExceeded)
	}

	select {
	case err := <-done:
		return err
	case <-o.ctx.Done():
		return context.Canceled
	case <-timeout:
		o.log.Errorw("media orchestrator dispatch operation timed out", nil, "timeout", DispatchTimeout)
		return fmt.Errorf("media orchestrator dispatch operation timed out after %v: %w", DispatchTimeout, context.DeadlineExceeded)
	}
}

func (o *MediaOrchestrator) dispatchLoop() {
	o.dispatchOK.Store(true)
	defer o.dispatchOK.Store(false)
	defer o.wg.Done()
	defer o.log.Debugw("media orchestrator dispatch loop exited")

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	mu := sync.Mutex{}

	for {
		select {
		case <-o.ctx.Done():
			mu.Lock()
			o.log.Debugw("media orchestrator dispatch loop exiting")
			// Drain pending cleanup before closing
			o.drainCleanup()
			if err := o.close(); err != nil {
				o.log.Errorw("error closing media orchestrator", err)
			}
			mu.Unlock()
			return
		case op := <-o.dispatchCH:
			mu.Lock()
			err := op.fn()
			op.done <- err
			mu.Unlock()
		case cleanup := <-o.cleanupCH:
			// Deferred element teardown, same OS-locked thread
			cleanup()
		}
	}
}

// scheduleCleanup sends a cleanup function to the dispatch loop for deferred execution.
// Falls back to inline execution if the channel is full.
func (o *MediaOrchestrator) scheduleCleanup(fn func()) {
	select {
	case o.cleanupCH <- fn:
	default:
		o.log.Warnw("cleanup channel full, executing inline", nil)
		fn()
	}
}

// drainCleanup executes all pending cleanup functions before shutdown.
func (o *MediaOrchestrator) drainCleanup() {
	for {
		select {
		case cleanup := <-o.cleanupCH:
			cleanup()
		default:
			return
		}
	}
}

func (o *MediaOrchestrator) close() error {
	var bfcpErr error
	if o.bfcp != nil {
		bfcpErr = o.bfcp.Close()
	}
	var screenshareErr error
	if o.screenshare != nil {
		screenshareErr = o.screenshare.Close()
	}
	err := errors.Join(
		o.camera.Close(),
		o.tracks.Close(),
		bfcpErr,
		screenshareErr,
	)
	o.cancel()

	return err
}

func (o *MediaOrchestrator) Close() error {
	if o.cancel == nil {
		return nil
	}
	o.cancel()
	o.wg.Wait()

	log := o.log
	*o = MediaOrchestrator{}
	pipeline.ForceMemoryRelease()
	log.Debugw("media orchestrator closed")

	return nil
}

func (o *MediaOrchestrator) AnswerSDP(offer *sdpv2.SDP) (answer *sdpv2.SDP, err error) {
	if err := o.okStates(MediaStateFailed, MediaStateOK, MediaStateReady, MediaStateStarted); err != nil {
		return nil, err
	}
	if err := o.dispatch(func() error {
		answer, err = o.answerSDP(offer)
		return err
	}); err != nil {
		return nil, err
	}
	return answer, nil
}
func (o *MediaOrchestrator) answerSDP(offer *sdpv2.SDP) (*sdpv2.SDP, error) {
	o.log.Debugw("answering sdp", "offer", offer)
	if offer.Audio == nil {
		return nil, fmt.Errorf("no audio in offer")
	}

	if err := offer.Audio.SelectCodec(); err != nil {
		return nil, fmt.Errorf("could not select audio codec: %w", err)
	}
	o.log.Debugw("selected audio codec", "codec", offer.Audio.Codec)

	o.audioinfo.SetMedia(offer.Audio)

	if offer.Video != nil {
		if err := offer.Video.SelectCodec(); err != nil {
			return nil, fmt.Errorf("could not select video codec: %w", err)
		}
		o.log.Debugw("selected video codec", "codec", offer.Video.Codec)
	}

	if offer.Screenshare != nil {
		if err := offer.Screenshare.SelectCodec(); err != nil {
			return nil, fmt.Errorf("could not select screenshare codec: %w", err)
		}
		o.log.Debugw("selected screenshare codec", "codec", offer.Screenshare.Codec)
	}

	o.sdp = offer

	if err := o.setupSDP(offer); err != nil {
		o.log.Errorw("could not setup sdp", err)
		return nil, fmt.Errorf("could not setup sdp: %w", err)
	}
	o.log.Debugw("setup sdp complete")

	answer, err := o.offerSDP(offer.Video != nil, offer.BFCP != nil, offer.Screenshare != nil)
	if err != nil {
		return nil, fmt.Errorf("could not create answer sdp: %w", err)
	}

	// Structured SDP answer logging (enabled via SIP_SDP_DEBUG=true)
	logSDPAnswer(o.log, answer)

	o.log.Debugw("created answer sdp", "answer", answer)

	o.state = MediaStateReady

	return answer, nil
}

func (o *MediaOrchestrator) offerSDP(camera bool, bfcp bool, screenshare bool) (*sdpv2.SDP, error) {
	builder := (&sdpv2.SDP{}).Builder()

	builder.SetAddress(o.opts.IP)

	// Copy m-line ordering from the remote offer (RFC 3264)
	if o.sdp != nil && len(o.sdp.MLineOrder) > 0 {
		builder.SetMLineOrder(o.sdp.MLineOrder)
		builder.SetUnknownMedia(o.sdp.UnknownMedia)
	}

	// Screenshare mstream ID from offer's label, BFCP mstrm, or default constant
	screenshareMStreamID := uint16(ScreenshareMSTreamID)
	if o.sdp != nil && o.sdp.Screenshare != nil && o.sdp.Screenshare.Label > 0 {
		screenshareMStreamID = o.sdp.Screenshare.Label
	} else if o.sdp != nil && o.sdp.BFCP != nil && o.sdp.BFCP.MStreamID > 0 {
		screenshareMStreamID = o.sdp.BFCP.MStreamID
	}

	// audio is required anyway
	builder.SetAudio(func(b *sdpv2.SDPMediaBuilder) (*sdpv2.SDPMedia, error) {
		codec := o.audioinfo.Codec()
		if codec == nil {
			for _, c := range o.audioinfo.AvailableCodecs() {
				b.AddCodec(func(_ *sdpv2.CodecBuilder) (*sdpv2.Codec, error) {
					return c, nil
				}, false)
			}
			// Add DTMF codec when no existing SDP is available
			b.AddDTMFCodec()
		} else {
			b.AddCodec(func(_ *sdpv2.CodecBuilder) (*sdpv2.Codec, error) {
				return codec, nil
			}, true)
			// Copy DTMF codec from offer if present, otherwise add it
			if o.sdp != nil {
				b.CopyDTMFCodec(o.sdp.Audio)
			} else {
				b.AddDTMFCodec()
			}
		}
		return b.
			SetRTPPort(uint16(o.audioinfo.Port())).
			Build()
	}).Build()

	if bfcp && o.bfcp != nil && screenshare {
		builder.SetBFCP(func(b *sdpv2.SDPBfcpBuilder) (*sdpv2.SDPBfcp, error) {
			proto := sdpv2.BfcpProtoUDP
			if o.bfcp.transport == BFCPTransportTCP {
				proto = sdpv2.BfcpProtoTCP
			}
			setup := sdpv2.BfcpSetupPassive
			if o.sdp != nil && o.sdp.BFCP != nil {
				proto = o.sdp.BFCP.Proto
				setup = o.sdp.BFCP.Setup.Reverse()
			}

			return b.
				SetPort(o.bfcp.Port()).
				SetConnectionAddr(o.opts.IP).
				SetConnection(sdpv2.BfcpConnectionNew).
				SetProto(proto).
				SetFloorCtrl(sdpv2.BfcpFloorCtrlServer).
				SetSetup(setup).
				SetConfID(o.bfcp.config.ConferenceID).
				SetUserID(1).
				SetFloorID(ContentFloorID).
				SetMStreamID(screenshareMStreamID).
				Build()
		})
	} else if bfcp && o.bfcp != nil && !screenshare {
		// BFCP server is running but screenshare is not yet active.
		// Advertise the real BFCP port so the remote can establish floor control
		// and later send a re-INVITE with screenshare.
		builder.SetBFCP(func(b *sdpv2.SDPBfcpBuilder) (*sdpv2.SDPBfcp, error) {
			proto := sdpv2.BfcpProtoUDP
			if o.bfcp.transport == BFCPTransportTCP {
				proto = sdpv2.BfcpProtoTCP
			}
			setup := sdpv2.BfcpSetupPassive
			if o.sdp != nil && o.sdp.BFCP != nil {
				proto = o.sdp.BFCP.Proto
				setup = o.sdp.BFCP.Setup.Reverse()
			}

			return b.
				SetPort(o.bfcp.Port()).
				SetConnectionAddr(o.opts.IP).
				SetConnection(sdpv2.BfcpConnectionNew).
				SetProto(proto).
				SetFloorCtrl(sdpv2.BfcpFloorCtrlServer).
				SetSetup(setup).
				SetConfID(o.bfcp.config.ConferenceID).
				SetUserID(1).
				SetFloorID(ContentFloorID).
				SetMStreamID(screenshareMStreamID).
				Build()
		})
	} else if bfcp && !screenshare {
		// BFCP in offer but no server started — include disabled m-line for RFC 3264 alignment.
		builder.SetBFCP(func(b *sdpv2.SDPBfcpBuilder) (*sdpv2.SDPBfcp, error) {
			proto := sdpv2.BfcpProtoTCP
			if o.sdp != nil && o.sdp.BFCP != nil {
				proto = o.sdp.BFCP.Proto
			}
			return b.
				SetDisabled(true).
				SetProto(proto).
				Build()
		})
	}

	if camera {
		builder.SetVideo(func(b *sdpv2.SDPMediaBuilder) (*sdpv2.SDPMedia, error) {
			codec := o.camera.Codec()
			if codec == nil {
				for _, c := range o.camera.SupportedCodecs() {
					b.AddCodec(func(_ *sdpv2.CodecBuilder) (*sdpv2.Codec, error) {
						return c, nil
					}, false)
				}
			} else {
				b.AddCodec(func(_ *sdpv2.CodecBuilder) (*sdpv2.Codec, error) {
					return codec, nil
				}, true)
			}
			b.SetDisabled(o.camera.Status() < VideoStatusReady)
			b.SetRTPPort(uint16(o.camera.RtpPort()))
			b.SetRTCPPort(uint16(o.camera.RtcpPort()))
			b.SetDirection(o.camera.Direction())
			return b.Build()
		})
	}

	if screenshare && o.screenshare != nil {
		builder.SetScreenshare(func(b *sdpv2.SDPMediaBuilder) (*sdpv2.SDPMedia, error) {
			codec := o.screenshare.Codec()
			if codec == nil {
				for _, c := range o.screenshare.SupportedCodecs() {
					b.AddCodec(func(_ *sdpv2.CodecBuilder) (*sdpv2.Codec, error) {
						return c, nil
					}, false)
				}
			} else {
				b.AddCodec(func(_ *sdpv2.CodecBuilder) (*sdpv2.Codec, error) {
					return codec, nil
				}, true)
			}
			b.SetRTPPort(uint16(o.screenshare.RtpPort()))
			b.SetRTCPPort(uint16(o.screenshare.RtcpPort()))
			b.SetDirection(sdpv2.DirectionSendRecv)
			b.SetLabel(screenshareMStreamID)
			return b.Build()
		})
	}

	offer, err := builder.Build()
	if err != nil {
		return nil, fmt.Errorf("could create a new sdp: %w", err)
	}
	o.log.Debugw("created offer sdp", "offer", offer)

	return offer, nil
}

// CreateOfferSDP generates an SDP offer for delayed offer (INVITE without SDP).
func (o *MediaOrchestrator) CreateOfferSDP() (offer *sdpv2.SDP, err error) {
	if err := o.okStates(MediaStateFailed, MediaStateOK, MediaStateReady, MediaStateStarted); err != nil {
		return nil, err
	}
	if err := o.dispatch(func() error {
		offer, err = o.createOfferSDP()
		return err
	}); err != nil {
		return nil, err
	}
	return offer, nil
}

func (o *MediaOrchestrator) createOfferSDP() (*sdpv2.SDP, error) {
	o.log.Debugw("creating offer sdp for delayed offer scenario")

	if o.bfcp == nil {
		o.setupBFCP(nil, netip.Addr{})
	}

	offer, err := o.offerSDP(true, true, true)
	if err != nil {
		return nil, fmt.Errorf("could not create offer sdp: %w", err)
	}

	// Set video to sendrecv with Cisco-compatible H264 codecs
	if offer.Video != nil {
		offer.Video.Disabled = false
		offer.Video.Direction = sdpv2.DirectionSendRecv
		setDelayedOfferH264Codecs(offer.Video)
	}

	if offer.Screenshare != nil {
		setDelayedOfferH264Codecs(offer.Screenshare)
	}

	// Cisco 5-m-line layout: audio, video(main), BFCP, video(slides), H224(rejected)
	offer.MLineOrder = []sdpv2.MLineType{
		sdpv2.MLineAudio,
		sdpv2.MLineVideo,
		sdpv2.MLineBFCP,
		sdpv2.MLineScreenshare,
		sdpv2.MLineUnknown,
	}
	offer.UnknownMedia = []*sdpv2.SDPMedia{{
		Kind:      sdpv2.MediaKindApplication,
		Disabled:  true,
		Port:      0,
		Direction: sdpv2.DirectionInactive,
		Codecs: []*sdpv2.Codec{{
			PayloadType: 0,
			Name:        "PCMU/8000",
		}},
	}}

	o.delayedOfferSDP = offer

	logSDPOffer(o.log, offer)

	return offer, nil
}

// setDelayedOfferH264Codecs replaces video codecs with H264 packetization-mode=0 (PT 97)
// and packetization-mode=1 (PT 126) using Cisco-compatible FMTP parameters.
func setDelayedOfferH264Codecs(m *sdpv2.SDPMedia) {
	fmtpBase := map[string]string{
		"profile-level-id": "428016",
		"max-fs":           "32400",
		"max-mbps":         "490000",
	}

	// Replace existing codecs with both mode=0 and mode=1 variants
	var codecs []*sdpv2.Codec
	for _, c := range m.Codecs {
		if c.ClockRate != 90000 {
			codecs = append(codecs, c)
			continue
		}

		// PT 97: packetization-mode=0
		fmtp0 := make(map[string]string, len(fmtpBase)+1)
		for k, v := range fmtpBase {
			fmtp0[k] = v
		}
		fmtp0["packetization-mode"] = "0"
		codecs = append(codecs, &sdpv2.Codec{
			PayloadType: 97,
			Name:        c.Name,
			Codec:       c.Codec,
			ClockRate:   c.ClockRate,
			FMTP:        fmtp0,
		})

		// PT 126: packetization-mode=1
		fmtp1 := make(map[string]string, len(fmtpBase)+1)
		for k, v := range fmtpBase {
			fmtp1[k] = v
		}
		fmtp1["packetization-mode"] = "1"
		codecs = append(codecs, &sdpv2.Codec{
			PayloadType: 126,
			Name:        c.Name,
			Codec:       c.Codec,
			ClockRate:   c.ClockRate,
			FMTP:        fmtp1,
		})
	}
	m.Codecs = codecs
}

// ApplyAnswerSDP applies an SDP answer received in ACK for delayed offer scenarios.
func (o *MediaOrchestrator) ApplyAnswerSDP(answer *sdpv2.SDP) error {
	if err := o.okStates(MediaStateFailed, MediaStateOK, MediaStateReady, MediaStateStarted); err != nil {
		return err
	}
	return o.dispatch(func() error {
		return o.applyAnswerSDP(answer)
	})
}

func (o *MediaOrchestrator) applyAnswerSDP(answer *sdpv2.SDP) error {
	o.log.Infow("delayed-offer: applying answer SDP",
		"hasAudio", answer.Audio != nil,
		"hasVideo", answer.Video != nil,
		"hasScreenshare", answer.Screenshare != nil,
		"hasBFCP", answer.BFCP != nil,
		"mlineOrder", answer.MLineOrder,
		"unknownCount", len(answer.UnknownMedia),
		"addr", answer.Addr,
	)

	if answer.BFCP != nil {
		o.log.Infow("delayed-offer: answer BFCP",
			"port", answer.BFCP.Port,
			"proto", answer.BFCP.Proto,
			"setup", answer.BFCP.Setup,
			"floorCtrl", answer.BFCP.FloorCtrl,
			"confID", answer.BFCP.ConfID,
			"disabled", answer.BFCP.Disabled,
		)
	}

	if answer.Screenshare != nil {
		o.log.Infow("delayed-offer: answer screenshare",
			"port", answer.Screenshare.Port,
			"content", answer.Screenshare.Content,
			"direction", answer.Screenshare.Direction,
			"disabled", answer.Screenshare.Disabled,
			"codecCount", len(answer.Screenshare.Codecs),
		)
	}

	if answer.Video != nil {
		o.log.Infow("delayed-offer: answer video",
			"port", answer.Video.Port,
			"content", answer.Video.Content,
			"direction", answer.Video.Direction,
			"disabled", answer.Video.Disabled,
			"codecCount", len(answer.Video.Codecs),
		)
	}

	for i, um := range answer.UnknownMedia {
		o.log.Infow("delayed-offer: unknown media",
			"index", i,
			"kind", um.Kind,
			"port", um.Port,
			"disabled", um.Disabled,
		)
	}

	if answer.Audio == nil {
		return fmt.Errorf("no audio in answer")
	}

	if err := answer.Audio.SelectCodec(); err != nil {
		return fmt.Errorf("could not select audio codec from answer: %w", err)
	}
	o.log.Infow("delayed-offer: selected audio codec", "codec", answer.Audio.Codec.Name, "pt", answer.Audio.Codec.PayloadType)

	o.audioinfo.SetMedia(answer.Audio)
	o.sdp = answer

	if answer.Video != nil {
		if err := answer.Video.SelectCodec(); err != nil {
			return fmt.Errorf("could not select video codec from answer: %w", err)
		}
		o.log.Infow("delayed-offer: selected video codec", "codec", answer.Video.Codec.Name, "pt", answer.Video.Codec.PayloadType)
	}

	if answer.Screenshare != nil {
		if err := answer.Screenshare.SelectCodec(); err != nil {
			return fmt.Errorf("could not select screenshare codec from answer: %w", err)
		}
		o.log.Infow("delayed-offer: selected screenshare codec", "codec", answer.Screenshare.Codec.Name, "pt", answer.Screenshare.Codec.PayloadType)
	}

	// Override answer PTs with the PTs from our delayed offer
	if o.delayedOfferSDP != nil {
		o.remapDelayedOfferPayloadTypes(answer)
	}

	// Fill missing BFCP answer fields from our delayed offer.
	if answer.BFCP != nil && o.delayedOfferSDP != nil && o.delayedOfferSDP.BFCP != nil {
		o.inferBFCPAnswerDefaults(answer.BFCP, o.delayedOfferSDP.BFCP)
	}

	if err := o.setupSDP(answer); err != nil {
		o.log.Errorw("could not setup sdp from answer", err)
		return fmt.Errorf("could not setup sdp from answer: %w", err)
	}

	o.state = MediaStateReady
	o.log.Infow("delayed-offer: answer applied, state=Ready")

	return nil
}

// remapDelayedOfferPayloadTypes overrides answer codec PTs with the matching
// offer PTs, selecting by packetization-mode.
func (o *MediaOrchestrator) remapDelayedOfferPayloadTypes(answer *sdpv2.SDP) {
	offer := o.delayedOfferSDP

	remapPT := func(label string, answerMedia, offerMedia *sdpv2.SDPMedia) {
		if answerMedia == nil || answerMedia.Codec == nil || offerMedia == nil || len(offerMedia.Codecs) == 0 {
			return
		}
		// Match offer codec by packetization-mode
		answerMode := answerMedia.Codec.FMTP["packetization-mode"]
		offerPT := offerMedia.Codecs[0].PayloadType
		for _, c := range offerMedia.Codecs {
			if c.FMTP["packetization-mode"] == answerMode {
				offerPT = c.PayloadType
				break
			}
		}
		if answerMedia.Codec.PayloadType != offerPT {
			o.log.Infow("delayed-offer: remapping "+label+" PT",
				"answerPT", answerMedia.Codec.PayloadType,
				"offerPT", offerPT,
				"packetizationMode", answerMode,
			)
			answerMedia.Codec.PayloadType = offerPT
		}
	}

	remapPT("video", answer.Video, offer.Video)
	remapPT("screenshare", answer.Screenshare, offer.Screenshare)
}

// inferBFCPAnswerDefaults fills missing BFCP answer fields from our offer
// when the answer only contains floorctrl and a port.
func (o *MediaOrchestrator) inferBFCPAnswerDefaults(answer *sdpv2.SDPBfcp, offer *sdpv2.SDPBfcp) {
	if answer.FloorCtrl != sdpv2.BfcpFloorCtrlClient && answer.FloorCtrl != sdpv2.BfcpFloorCtrlBoth {
		return
	}

	inferred := false

	if answer.Setup == "" {
		answer.Setup = sdpv2.BfcpSetupActive
		inferred = true
	}
	if answer.Connection == "" {
		answer.Connection = sdpv2.BfcpConnectionNew
		inferred = true
	}
	if answer.ConfID == 0 && offer.ConfID != 0 {
		answer.ConfID = offer.ConfID
		inferred = true
	}
	if answer.FloorID == 0 && offer.FloorID != 0 {
		answer.FloorID = offer.FloorID
		inferred = true
	}
	if answer.MStreamID == 0 && offer.MStreamID != 0 {
		answer.MStreamID = offer.MStreamID
		inferred = true
	}
	if answer.UserID == 0 && offer.UserID != 0 {
		answer.UserID = offer.UserID + 1
		inferred = true
	}

	if inferred {
		o.log.Infow("delayed-offer: inferred missing BFCP answer fields from offer",
			"setup", answer.Setup,
			"connection", answer.Connection,
			"confID", answer.ConfID,
			"userID", answer.UserID,
			"floorID", answer.FloorID,
			"mStreamID", answer.MStreamID,
		)
	}
}

func (o *MediaOrchestrator) setupSDP(sdp *sdpv2.SDP) error {
	o.log.Infow("setupSDP: reconciling camera",
		"videoNil", sdp.Video == nil,
		"cameraStatus", o.camera.Status(),
	)
	if _, err := o.camera.Reconcile(sdp.Addr, sdp.Video); err != nil {
		o.log.Errorw("could not reconcile video sdp", err)
		return fmt.Errorf("could not reconcile video sdp: %w", err)
	}
	o.log.Infow("setupSDP: camera reconciled", "cameraStatus", o.camera.Status())

	// Update audio RTP destination if port changed
	if o.sdp != nil && o.sdp.Audio != nil && sdp.Audio != nil && o.sdp.Audio.Port != sdp.Audio.Port {
		o.log.Infow("updating audio RTP destination",
			"oldPort", o.sdp.Audio.Port, "newPort", sdp.Audio.Port)
		o.audioinfo.SetDst(netip.AddrPortFrom(sdp.Addr, sdp.Audio.Port))
	}

	o.log.Infow("setupSDP: BFCP check",
		"sdpBFCP", sdp.BFCP != nil,
		"existingBFCP", o.bfcp != nil,
	)
	if sdp.BFCP != nil && o.bfcp == nil {
		o.setupBFCP(sdp.BFCP, sdp.Addr)
	}

	// Reconcile screenshare when BFCP is active
	bfcpAvailable := sdp.BFCP != nil || o.bfcp != nil
	o.log.Infow("setupSDP: screenshare check",
		"bfcpAvailable", bfcpAvailable,
		"screenshareManager", o.screenshare != nil,
		"sdpScreenshare", sdp.Screenshare != nil,
		"sdpVideo", sdp.Video != nil,
	)
	if bfcpAvailable && o.screenshare != nil && sdp.Screenshare != nil {
		screenshareMedia := sdp.Screenshare
		o.log.Infow("setupSDP: reconciling screenshare",
			"usingScreenshareMedia", sdp.Screenshare != nil,
			"mediaPort", screenshareMedia.Port,
			"mediaContent", screenshareMedia.Content,
			"mediaDirection", screenshareMedia.Direction,
			"screenshareStatus", o.screenshare.Status(),
		)
		if _, err := o.screenshare.Reconcile(sdp.Addr, screenshareMedia); err != nil {
			o.log.Errorw("could not reconcile screenshare sdp", err)
			return fmt.Errorf("could not reconcile screenshare sdp: %w", err)
		}
		o.log.Infow("setupSDP: screenshare reconciled", "screenshareStatus", o.screenshare.Status())
	} else {
		o.log.Infow("setupSDP: screenshare reconciliation SKIPPED")
	}

	return nil
}

func (o *MediaOrchestrator) setupBFCP(bfcpOffer *sdpv2.SDPBfcp, sessionAddr netip.Addr) {
	transport := BFCPTransportUDP
	if bfcpOffer != nil && bfcpOffer.Proto != sdpv2.BfcpProtoUDP {
		transport = BFCPTransportTCP
	}

	o.bfcp = NewBFCPManager(o.ctx, o.log, o.opts, transport, bfcpOffer, sessionAddr)

	if o.bfcp != nil {
		o.bfcp.OnFloorGranted = func(floorID, userID uint16) {
			if userID == VirtualClientUserID {
				o.screenshare.SetFloorHeld(true)
			}
		}
		o.bfcp.OnFloorReleased = func(floorID, userID uint16) {
			if userID == VirtualClientUserID {
				o.screenshare.SetFloorHeld(false)
			}
		}
	}
}

func (o *MediaOrchestrator) start() error {
	o.log.Infow("start: beginning",
		"cameraStatus", o.camera.Status(),
		"screenshareNil", o.screenshare == nil,
		"bfcpNil", o.bfcp == nil,
		"state", o.state,
	)
	if o.screenshare != nil {
		o.log.Infow("start: screenshare state",
			"status", o.screenshare.Status(),
			"isReady", o.screenshare.IsReady(),
			"isActive", o.screenshare.IsActive(),
			"hasFloor", o.screenshare.HasFloor(),
		)
	}
	if o.bfcp != nil {
		o.log.Infow("start: BFCP state",
			"transport", o.bfcp.transport,
			"isActive", o.bfcp.isActive,
			"port", o.bfcp.Port(),
		)
	}

	if o.camera.Status() == VideoStatusReady {
		o.log.Infow("start: starting camera")
		if err := o.camera.Start(); err != nil {
			o.log.Errorw("could not start camera", err)
			return fmt.Errorf("could not start camera: %w", err)
		}
	}

	if o.screenshare != nil && o.screenshare.Status() == VideoStatusReady {
		o.log.Infow("start: starting screenshare")
		if err := o.screenshare.Start(); err != nil {
			o.log.Errorw("could not start screenshare", err)
			return fmt.Errorf("could not start screenshare: %w", err)
		}
		o.log.Infow("start: screenshare started", "status", o.screenshare.Status())
	} else if o.screenshare != nil {
		o.log.Infow("start: screenshare NOT started", "status", o.screenshare.Status())
	}

	if o.bfcp != nil {
		o.log.Infow("start: connecting BFCP to remote", "isActive", o.bfcp.isActive)
		if err := o.bfcp.ConnectToRemote(); err != nil {
			o.log.Warnw("failed to connect to remote BFCP server", err)
		}
	}

	o.state = MediaStateStarted
	o.log.Infow("start: complete, state=Started")
	return nil
}

func (o *MediaOrchestrator) Start() (err error) {
	if err := o.okStates(MediaStateReady); err != nil {
		return err
	}
	if err := o.dispatch(func() error {
		return o.start()
	}); err != nil {
		return err
	}
	return nil
}
