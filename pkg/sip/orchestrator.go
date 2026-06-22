package sip

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/media-sdk/dtmf"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/sip/pkg/sip/pipeline"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/livekitcompositor"
	"github.com/livekit/sipgo/sip"
)

var (
	ErrWrongState = errors.New("media orchestrator in wrong state")
)

const (
	RtpMediaTimeout              = 30 * time.Second
	RtpMediaTimeoutCheckInterval = 10 * time.Second
)

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

type CallHandler interface {
	SendReInvite(ctx context.Context, offer []byte) (*sip.Response, error)
	ResetRtpTimeout()
}

type MediaOrchestrator struct {
	ctx       context.Context
	cancel    context.CancelFunc
	log       logger.Logger
	sipCallID string
	opts      *MediaOptions
	handler   CallHandler
	closed    atomic.Bool

	pipeline *pipeline.Pipeline

	stats            atomic.Pointer[pipeline.CallStats]
	totalRxBytes     int64
	totalTxBytesTime time.Time
	lastSr           time.Time

	state MediaState
	wg    sync.WaitGroup

	dtmfHandler func(ev dtmf.Event)
}

func NewMediaOrchestrator(log logger.Logger, ctx context.Context, handler CallHandler, sipCallID string, opts *MediaOptions) (*MediaOrchestrator, error) {
	ctx, cancel := context.WithCancel(ctx)
	o := &MediaOrchestrator{
		ctx:       ctx,
		cancel:    cancel,
		log:       log,
		sipCallID: sipCallID,
		opts:      opts,
		handler:   handler,
		state:     MediaStateNew,
	}

	if err := o.init(); err != nil {
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
		Lang:                  o.opts.Lang,
		MaxActiveParticipants: o.opts.MaxActiveParticipants,
		Gst:                   o.opts.Gst,
		PublishCodecs:         o.opts.PublishCodecs,
	}, o.sipCallID)
	if err != nil {
		return fmt.Errorf("could not create pipeline: %w", err)
	}
	o.pipeline = p

	o.wg.Add(1)
	go o.loopEvents()

	if err := o.pipeline.SetState(gst.StateReady); err != nil {
		return fmt.Errorf("failed to set pipeline to ready state: %w", err)
	}
	o.rtpTimeout()

	o.state = MediaStateOK

	return nil
}

func (o *MediaOrchestrator) isRtpStalled() bool {
	o.UpdateStats()
	if o.stats.Load() == nil {
		return false
	}
	if o.stats.Load().OnHold {
		return false
	}
	if (o.totalTxBytesTime.IsZero() || time.Since(o.totalTxBytesTime) > RtpMediaTimeout) && (o.lastSr.IsZero() || time.Since(o.lastSr) > RtpMediaTimeout) {
		return true
	}
	return false
}

func (o *MediaOrchestrator) rtpTimeout() {
	ticker := time.NewTicker(RtpMediaTimeoutCheckInterval)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-o.ctx.Done():
				return
			case <-ticker.C:
				if !o.isRtpStalled() {
					o.handler.ResetRtpTimeout()
				}
			}
		}
	}()
}

func (o *MediaOrchestrator) UpdateStats() {
	if o.pipeline != nil {
		stats, err := o.pipeline.GetStats()
		if err != nil {
			o.log.Errorw("failed to get pipeline stats", err)
			return
		}
		if stats == nil {
			return
		}
		if stats.TotalRxBytes > o.totalRxBytes {
			o.totalRxBytes = stats.TotalRxBytes
			o.totalTxBytesTime = time.Now()
		}
		if stats.LastSR.After(o.lastSr) {
			o.lastSr = stats.LastSR
		}
		o.stats.Store(stats)
	}
}

func (o *MediaOrchestrator) Stats() *pipeline.CallStats {
	stats := o.stats.Load()
	if stats == nil {
		o.UpdateStats()
		stats = o.stats.Load()
	}
	return stats
}

func (o *MediaOrchestrator) okStates(allowed ...MediaState) error {
	if slices.Contains(allowed, o.state) {
		return nil
	}
	return fmt.Errorf("invalid state: %s, expected one of %v: %w", o.state, allowed, ErrWrongState)
}

func (o *MediaOrchestrator) close() {
	o.wg.Go(func() {
		if err := o.pipeline.Close(); err != nil {
			o.log.Errorw("failed to close pipeline", err)
		}
	})
}

func (o *MediaOrchestrator) Close() error {
	o.cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		o.wg.Wait()
	}()

	select {
	case <-done:
		pipeline.ForceMemoryRelease()
		o.log.Debugw("media orchestrator closed")
		return nil
	case <-time.After(2 * time.Minute):
		o.log.Warnw("timeout waiting for media orchestrator to close", nil)
		return fmt.Errorf("timeout waiting for media orchestrator to close")
	}
}

func (o *MediaOrchestrator) NewOffer() ([]byte, error) {
	// if err := o.okStates(MediaStateOK, MediaStateReady); err != nil {
	// 	return nil, err
	// }
	offer, err := o.pipeline.EmitCreateOfferSDP()
	if err != nil {
		return nil, fmt.Errorf("failed to emit create-offer-sdp: %w", err)
	}
	return []byte(offer), nil
}

func (o *MediaOrchestrator) AnswerSDP(offer []byte) (answer []byte, err error) {
	if err := o.okStates(MediaStateOK, MediaStateReady, MediaStateStarted); err != nil {
		return nil, err
	}
	return o.answerSDP(offer)
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

func (o *MediaOrchestrator) SetAnswerSDP(answer []byte) error {
	if err := o.okStates(MediaStateFailed, MediaStateOK, MediaStateReady, MediaStateStarted); err != nil {
		return err
	}
	if err := o.pipeline.EmitAnswerSDP(string(answer)); err != nil {
		return err
	}
	// Outbound offer/answer negotiation is now complete: we created the offer
	// (NewOffer) and have applied the remote's answer. Advance to Ready so
	// Start() can move the pipeline to PLAYING, mirroring answerSDP() on the
	// inbound path. Without this the orchestrator stays in OK and Start()'s
	// okStates(MediaStateReady) check rejects it, leaving the pipeline in READY.
	o.state = MediaStateReady
	return nil
}

func (o *MediaOrchestrator) AckSDP(req *sip.Request, tx sip.ServerTransaction) error {
	if err := o.okStates(MediaStateOK, MediaStateReady, MediaStateStarted); err != nil {
		return err
	}

	return o.ackSDP(req, tx)
}

func (o *MediaOrchestrator) ackSDP(req *sip.Request, _ sip.ServerTransaction) error {
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

func (o *MediaOrchestrator) loopEvents() {
	defer o.wg.Done()
	for {
		select {
		case <-o.ctx.Done():
			o.close()
			return
		case offer := <-o.pipeline.SendOfferCh():
			if err := o.handleSendOffer(offer); err != nil {
				o.log.Errorw("failed to handle send-offer-sdp", err)
			}
		case nb, ok := <-o.pipeline.DTMF():
			if !ok {
				continue
			}
			digit, ok := dtmfMap[nb]
			if !ok {
				o.log.Warnw("Received invalid DTMF number", nil, "number", nb)
				continue
			}
			if o.dtmfHandler == nil {
				continue
			}
			// TODO: do we need to set the other fields?
			o.dtmfHandler(dtmf.Event{
				Code:  byte(nb),
				Digit: digit,
			})

		}
	}
}

// abortOffer releases the pipeline negotiation state held by a failed
// outgoing offer.
func (o *MediaOrchestrator) abortOffer() {
	if err := o.pipeline.EmitOfferAborted(); err != nil {
		o.log.Errorw("failed to abort pending offer", err)
	}
}

func (o *MediaOrchestrator) handleSendOffer(offer string) error {
	resp, err := o.handler.SendReInvite(o.ctx, []byte(offer))
	// Bail if the orchestrator was closed during SendReInvite.
	if o.ctx.Err() != nil {
		return nil
	}
	if err != nil {
		o.log.Errorw("re-INVITE failed", err)
		o.abortOffer()
		return err
	}

	if resp.StatusCode != 200 {
		o.abortOffer()
		// 491 Request Pending = glare: the device sent its own re-INVITE
		// concurrently with ours. Expected — our offer is rolled back and the
		// device's re-INVITE is handled on the inbound path. Not an error.
		if resp.StatusCode == 491 {
			o.log.Infow("re-INVITE superseded by concurrent device re-INVITE (glare); rolled back", "status", resp.StatusCode)
			return nil
		}
		o.log.Errorw("re-INVITE rejected", nil, "status", resp.StatusCode)
		return fmt.Errorf("re-INVITE rejected with status %d", resp.StatusCode)
	}

	answerSDP := string(resp.Body())
	if answerSDP == "" {
		o.log.Errorw("re-INVITE 200 OK has no SDP body", nil)
		o.abortOffer()
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
	return o.start()
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
	o.dtmfHandler = h
}

func (o *MediaOrchestrator) ShowMessage(message string, level gst.DebugLevel) {
	if o.pipeline != nil {
		o.pipeline.SetContext(livekitcompositor.NewContextOverlayMessage(message, level, true))
	}
}

func (o *MediaOrchestrator) HideMessage() {
	if o.pipeline != nil {
		o.pipeline.SetContext(livekitcompositor.NewContextOverlayMessage("", 0, false))
	}
}
