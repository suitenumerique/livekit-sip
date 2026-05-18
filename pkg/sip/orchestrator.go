package sip

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/media-sdk/dtmf"
	sdpv2 "github.com/livekit/media-sdk/sdp/v2"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/sip/pkg/sip/pipeline"
	"github.com/livekit/sipgo/sip"
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

	pipeline *pipeline.Pipeline

	stats atomic.Pointer[pipeline.CallStats]

	state MediaState
}

func NewMediaOrchestrator(log logger.Logger, ctx context.Context, inbound *sipInbound, opts *MediaOptions) (*MediaOrchestrator, error) {
	ctx, cancel := context.WithCancel(ctx)
	o := &MediaOrchestrator{
		ctx:        ctx,
		cancel:     cancel,
		log:        log,
		opts:       opts,
		inbound:    inbound,
		dispatchCH: make(chan dispatchOperation, 1),
		state:      MediaStateNew,
	}

	o.wg.Add(1)
	go o.dispatchLoop()
	for !o.dispatchOK.Load() {
		runtime.Gosched() // wait for dispatch loop to start
	}

	if err := o.dispatch(func() error {
		return o.init()
	}); err != nil {
		return nil, err
	}

	return o, nil
}

func (o *MediaOrchestrator) init() error {
	if err := o.okStates(MediaStateNew); err != nil {
		return err
	}

	p, err := pipeline.New(o.ctx, o.log, pipeline.SipOpt{
		IP:                    o.opts.IP.String(),
		PortStart:             uint16(o.opts.Ports.Start),
		PortEnd:               uint16(o.opts.Ports.End),
		VideoWidth:            o.opts.VideoWidth,
		VideoHeight:           o.opts.VideoHeight,
		Framerate:             o.opts.Framerate,
		MaxActiveParticipants: o.opts.MaxActiveParticipants,
		Gst:                   o.opts.Gst,
		PublishCodecs:         o.opts.PublishCodecs,
	}, o.inbound.sipCallID)
	if err != nil {
		return fmt.Errorf("could not create pipeline: %w", err)
	}
	o.pipeline = p
	o.pipeline.OnStats(func(stats *pipeline.CallStats) {
		o.stats.Store(stats)
	})

	o.wg.Add(1)
	go o.sendOfferLoop()

	if err := o.pipeline.SetStateWait(gst.StateReady); err != nil {
		return fmt.Errorf("failed to set pipeline to ready state: %w", err)
	}

	o.state = MediaStateOK

	return nil
}

func (o *MediaOrchestrator) UpdateStats() {
	if o.pipeline != nil && o.pipeline.Pipeline().GetCurrentState() == gst.StatePlaying {
		o.pipeline.GetStats()
	}
}

func (o *MediaOrchestrator) Stats() *pipeline.CallStats {
	return o.stats.Load()
}

func (o *MediaOrchestrator) okStates(allowed ...MediaState) error {
	for _, state := range allowed {
		if o.state == state {
			return nil
		}
	}
	return fmt.Errorf("invalid state: %s, expected one of %v: %w", o.state, allowed, ErrWrongState)
}

const DispatchTimeout = 200 * time.Second

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
	// if o.bfcp != nil {
	// 	bfcpErr = o.bfcp.Close()
	// }
	err := errors.Join(
		o.pipeline.Close(),
		bfcpErr,
	)
	o.cancel()

	return err
}

func (o *MediaOrchestrator) Close() error {
	o.cancel()
	o.wg.Wait()

	log := o.log
	o.pipeline = nil
	// *o = MediaOrchestrator{}
	pipeline.ForceMemoryRelease()
	log.Debugw("media orchestrator closed")

	return nil
}

// // GetRoom implements [RoomCallbacks].
// func (o *MediaOrchestrator) JoinRoom(wsUrl, token string, callbacks *lksdk.RoomCallback, opts ...lksdk.ConnectOption) (*lksdk.Room, error) {
// 	var errs []error
// 	room, err := o.pipeline.GetRoom()
// 	if err != nil || room == nil {
// 		return nil, fmt.Errorf("could not get room from pipeline: %w", err)
// 	}
// 	errs = append(errs, err)
// 	errs = append(errs, o.pipeline.SetRoomCallbacks(callbacks))
// 	errs = append(errs, o.pipeline.SetRoomOptions(wsUrl, token, opts...))
// 	if err := errors.Join(errs...); err != nil {
// 		return nil, fmt.Errorf("could not join room: %w", err)
// 	}
// 	return room, nil
// }

func (o *MediaOrchestrator) AnswerSDP(offer []byte) (answer []byte, err error) {
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

func (o *MediaOrchestrator) answerSDP(offerData []byte) ([]byte, error) {
	answerStr, err := o.pipeline.EmitOfferSDP(string(offerData))
	if err != nil {
		o.log.Errorw("failed to emit offer-sdp", err)
		return nil, err
	}
	if answerStr == "" {
		o.log.Errorw("offer-sdp returned an empty answer", nil)
		return nil, fmt.Errorf("offer-sdp returned an empty answer")
	}

	o.state = MediaStateReady

	return []byte(answerStr), nil
}

func (o *MediaOrchestrator) AckSDP(req *sip.Request, tx sip.ServerTransaction) error {
	if err := o.okStates(MediaStateFailed, MediaStateOK, MediaStateReady, MediaStateStarted); err != nil {
		return err
	}
	return o.dispatch(func() error {
		return o.ackSDP(req, tx)
	})
}

func (o *MediaOrchestrator) ackSDP(req *sip.Request, tx sip.ServerTransaction) error {
	sdp := req.Body()
	if sdp == nil {
		sdp = []byte{}
	}

	if err := o.pipeline.EmitAckSDP(string(sdp)); err != nil {
		o.log.Errorw("failed to emit ack-sdp", err)
		return err
	}

	o.log.Infow("ACKed SDP answer", "sdp", string(sdp))

	return nil
}

func (o *MediaOrchestrator) sendOfferLoop() {
	defer o.wg.Done()
	for {
		select {
		case <-o.ctx.Done():
			return
		case offer := <-o.pipeline.SendOfferCh():
			if err := o.handleSendOffer(offer); err != nil {
				o.log.Errorw("failed to handle send-offer-sdp", err)
			}
		}
	}
}

func (o *MediaOrchestrator) handleSendOffer(offer string) error {
	resp, err := o.inbound.sendReInvite(o.ctx, []byte(offer))
	if err != nil {
		o.log.Errorw("re-INVITE failed", err)
		return err
	}

	if resp.StatusCode != 200 {
		o.log.Errorw("re-INVITE rejected", nil, "status", resp.StatusCode)
		return fmt.Errorf("re-INVITE rejected with status %d", resp.StatusCode)
	}

	answerSDP := string(resp.Body())
	if answerSDP == "" {
		o.log.Errorw("re-INVITE 200 OK has no SDP body", nil)
		return fmt.Errorf("re-INVITE 200 OK has no SDP body")
	}

	if err := o.pipeline.EmitAnswerSDP(answerSDP); err != nil {
		o.log.Errorw("failed to emit answer-sdp after re-INVITE", err)
		return err
	}

	return nil
}

func (o *MediaOrchestrator) start() error {

	if err := o.pipeline.SetState(gst.StatePlaying); err != nil {
		return fmt.Errorf("failed to set pipeline to playing state: %w", err)
	}

	o.log.Infow("media orchestrator started")

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

	// o.wg.Add(1)
	// go func() {
	// 	defer o.wg.Done()
	// 	for {
	// 		select {
	// 		case <-o.ctx.Done():
	// 			return
	// 		case <-time.After(10 * time.Second):
	// 			o.pipeline.GetStats()
	// 		}
	// 	}
	// }()

	return nil
}

var dtmfMap = map[int]byte{
	0:  '0',
	1:  '1',
	2:  '2',
	3:  '3',
	4:  '4',
	5:  '5',
	6:  '6',
	7:  '7',
	8:  '8',
	9:  '9',
	10: '*',
	11: '#',
}

func (o *MediaOrchestrator) DtmfHandler(h func(ev dtmf.Event)) {
	go func() {
		for {
			select {
			case <-o.ctx.Done():
				return
			case nb, ok := <-o.pipeline.DTMF():
				if !ok {
					return
				}
				digit, ok := dtmfMap[nb]
				if !ok {
					o.log.Warnw("Received invalid DTMF number", nil, "number", nb)
					continue
				}
				// TODO: do we need to set the other fields?
				h(dtmf.Event{
					Code:  byte(nb),
					Digit: digit,
				})
			}
		}
	}()
}
