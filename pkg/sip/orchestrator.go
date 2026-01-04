package sip

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	sdpv2 "github.com/livekit/media-sdk/sdp/v2"
	"github.com/livekit/protocol/logger"
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
	dispatchOK atomic.Bool
	wg         sync.WaitGroup

	audioinfo   AudioInfo
	camera      *CameraManager
	screenshare *ScreenshareManager
	tracks      *TrackManager
	bfcp        *BFCPManager

	sdp   *sdpv2.SDP
	state MediaState
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

	o.bfcp = NewBFCPManager(o.ctx, o.log, o.opts)

	// Wire up BFCP callbacks to screenshare
	if o.bfcp != nil {
		o.bfcp.OnFloorGranted = func(floorID, userID uint16) {
			// Track when floor is granted (either to us or Poly)
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

	// Select screenshare codec if present
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

	answer, err := o.offerSDP(offer.Video != nil, offer.BFCP != nil, (offer.Screenshare != nil || (offer.Video != nil && offer.BFCP != nil)))
	if err != nil {
		return nil, fmt.Errorf("could not create answer sdp: %w", err)
	}
	o.log.Debugw("created answer sdp", "answer", answer)

	o.state = MediaStateReady

	return answer, nil
}

func (o *MediaOrchestrator) offerSDP(camera bool, bfcp bool, screenshare bool) (*sdpv2.SDP, error) {
	builder := (&sdpv2.SDP{}).Builder()

	builder.SetAddress(o.opts.IP)

	// audio is required anyway
	builder.SetAudio(func(b *sdpv2.SDPMediaBuilder) (*sdpv2.SDPMedia, error) {
		codec := o.audioinfo.Codec()
		if codec == nil {
			for _, c := range o.audioinfo.AvailableCodecs() {
				b.AddCodec(func(_ *sdpv2.CodecBuilder) (*sdpv2.Codec, error) {
					return c, nil
				}, false)
			}
		} else {
			b.AddCodec(func(_ *sdpv2.CodecBuilder) (*sdpv2.Codec, error) {
				return codec, nil
			}, true)
		}
		return b.
			SetRTPPort(uint16(o.audioinfo.Port())).
			Build()
	}).Build()

	if bfcp && o.bfcp != nil {
		if screenshare {
			builder.SetBFCP(func(b *sdpv2.SDPBfcpBuilder) (*sdpv2.SDPBfcp, error) {
				return b.
					SetPort(o.bfcp.Port()).
					SetConnection(sdpv2.BfcpConnectionNew).
					SetProto(sdpv2.BfcpProtoTCP).
					SetFloorCtrl(sdpv2.BfcpFloorCtrlServer).
					SetSetup(sdpv2.BfcpSetupPassive).
					SetConfID(o.bfcp.config.ConferenceID).
					SetUserID(1).
					SetMStreamID(ScreenshareMSTreamID).
					Build()
			})
		}
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
			// b.SetDisabled(false)
			b.SetRTPPort(uint16(o.camera.RtpPort()))
			b.SetRTCPPort(uint16(o.camera.RtcpPort()))
			b.SetDirection(o.camera.Direction())
			return b.Build()
		})
	}

	// Add screenshare m=video line with content:slides when BFCP is present
	if screenshare && o.screenshare != nil && o.screenshare.Status() >= VideoStatusReady {
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
			b.SetDirection(sdpv2.DirectionSendOnly) // We send screenshare to SIP
			b.SetLabel(ScreenshareMSTreamID)        // Match BFCP mstrm ID
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

func (o *MediaOrchestrator) setupSDP(sdp *sdpv2.SDP) error {
	o.log.Debugw("setting up sdp", "sdp", sdp)

	o.log.Debugw("reconciling camera")
	if _, err := o.camera.Reconcile(sdp.Addr, sdp.Video); err != nil {
		o.log.Errorw("could not reconcile video sdp", err)
		return fmt.Errorf("could not reconcile video sdp: %w", err)
	}

	// Reconcile screenshare when BFCP is present
	if sdp.BFCP != nil && o.screenshare != nil {
		o.log.Debugw("reconciling screenshare")
		// Use sdp.Screenshare if available (from re-INVITE), otherwise use sdp.Video for codec info
		screenshareMedia := sdp.Screenshare
		if screenshareMedia == nil {
			// Initial INVITE: use video media for codec negotiation only
			// The actual destination port won't be known until re-INVITE
			screenshareMedia = sdp.Video
		}
		if _, err := o.screenshare.Reconcile(sdp.Addr, screenshareMedia); err != nil {
			o.log.Errorw("could not reconcile screenshare sdp", err)
			return fmt.Errorf("could not reconcile screenshare sdp: %w", err)
		}
	}

	return nil
}

func (o *MediaOrchestrator) start() error {
	if o.camera.Status() == VideoStatusReady {
		o.log.Debugw("starting camera")
		if err := o.camera.Start(); err != nil {
			o.log.Errorw("could not start camera", err)
			return fmt.Errorf("could not start camera: %w", err)
		}
	}

	// Start screenshare if ready (BFCP was negotiated)
	if o.screenshare != nil && o.screenshare.Status() == VideoStatusReady {
		o.log.Debugw("starting screenshare")
		if err := o.screenshare.Start(); err != nil {
			o.log.Errorw("could not start screenshare", err)
			return fmt.Errorf("could not start screenshare: %w", err)
		}
	}

	o.state = MediaStateStarted
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
