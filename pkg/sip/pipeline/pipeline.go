package pipeline

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"time"

	"github.com/frostbyte73/core"
	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/sip/pkg/sip/pipeline/event"
)

type BasePipeline struct {
	log      logger.Logger
	pipeline *gst.Pipeline
	loop     *event.EventLoop
	ctx      context.Context
	closed   core.Fuse
	cleanup  func() error
}

type GstPipeline interface {
	Pipeline() *gst.Pipeline
	Log() logger.Logger
	Loop() *event.EventLoop
	SetState(state gst.State) error
	SetStateWait(state gst.State) error
	Close() error
	Closed() bool
}

type GstChain interface {
	Create() error
	Add() error
	Link() error
	Close() error
}

func (p *BasePipeline) Log() logger.Logger {
	return p.log
}

func (p *BasePipeline) Loop() *event.EventLoop {
	return p.loop
}

func (p *BasePipeline) Pipeline() *gst.Pipeline {
	return p.pipeline
}

func (p *BasePipeline) SetState(state gst.State) error {
	if p.Closed() {
		return fmt.Errorf("cannot set state on closed pipeline")
	}

	if state == gst.StateNull {
		return p.Close()
	}

	if err := p.Pipeline().SetState(state); err != nil {
		return fmt.Errorf("failed to set pipeline state: %w", err)
	}

	return nil
}

func (p *BasePipeline) SetStateWait(state gst.State) error {
	if p.Closed() {
		return fmt.Errorf("cannot set state on closed pipeline")
	}

	if state == gst.StateNull {
		return p.Close()
	}

	if err := p.Pipeline().SetState(state); err != nil {
		return fmt.Errorf("failed to set pipeline state: %w", err)
	}

	cr, s := p.Pipeline().GetState(state, gst.ClockTime(time.Second*30))
	if cr != gst.StateChangeSuccess {
		return fmt.Errorf("failed to change pipeline state, wanted %s got %s: %s", state.String(), s.String(), cr.String())
	}
	if s != state {
		return fmt.Errorf("pipeline did not reach desired state, wanted %s got %s", state.String(), s.String())
	}

	return nil
}

var pid = os.Getpid()

func (p *BasePipeline) Close() error {
	if p.Closed() {
		p.log.Debugw("Pipeline already closed")
		return nil
	}
	p.closed.Break()
	p.log.Debugw("Closing pipeline")

	defer p.loop.Stop()

	done := make(chan struct{})
	var err error
	go func() {
		defer close(done)
		p.log.Debugw("Setting pipeline to null state", "pid", pid)
		err = p.Pipeline().SetState(gst.StateNull)
		p.log.Debugw("Pipeline set to null state complete", "pid", pid, "err", err)
	}()

	closed := false
	select {
	case <-done:
		closed = true
	case <-time.After(10 * time.Second):
	}
	if !closed {
		p.Log().Warnw("Timeout waiting for pipeline to set to null state, sending flush event", nil)
		go func() {
			p.Pipeline().SendEvent(gst.NewFlushStartEvent())
		}()
		select {
		case <-done:
			closed = true
		case <-time.After(5 * time.Second):
		}
	}
	if !closed {
		p.Log().Warnw("Timeout waiting for pipeline to set to null state after flush start, sending flush stop event", nil)
		go func() {
			p.Pipeline().SendEvent(gst.NewFlushStopEvent(true))
		}()
		select {
		case <-done:
			closed = true
		case <-time.After(5 * time.Second):
		}
	}
	if !closed {
		p.Log().Warnw("Timeout waiting for pipeline to set to null state after flush stop, trying to break clock", nil)
		go func() {
			p.Pipeline().SetBaseTime(0)
			p.Pipeline().SetStartTime(gst.ClockTimeNone)
		}()
		select {
		case <-done:
			closed = true
		case <-time.After(5 * time.Second):
		}
	}

	if !closed && p.cleanup != nil {
		p.Log().Warnw("Failed to set pipeline to null state after breaking clock, trying early cleanup", nil)
		// earlyCleanup = true
		// cleanupfn()
		if err := p.cleanup(); err != nil {
			p.Log().Errorw("Failed timeout cleanup before setting pipeline to null state", err)
		}
		p.cleanup = nil // prevent double cleanup
		select {
		case <-done:
			closed = true
		case <-time.After(5 * time.Second):
		}
	}

	if !closed {
		p.Log().Errorw("Failed to set pipeline to null state after breaking clock", nil)
		return fmt.Errorf("failed to set pipeline to null state")
	}

	p.log.Debugw("Pipeline set to null state")

	if p.cleanup != nil {
		p.log.Debugw("Running pipeline cleanup")
		if err := p.cleanup(); err != nil {
			p.Log().Errorw("Failed timeout cleanup before setting pipeline to null state", err)
		}
		p.log.Debugw("Pipeline cleanup complete")
	}

	// if p.cleanup != nil || earlyCleanup {
	// 	var cleanupErr error
	// 	select {
	// 	case cleanupErr = <-cleanupErrCH:
	// 	case <-time.After(10 * time.Second):
	// 		p.Log().Warnw("Timeout waiting for pipeline cleanup to complete", nil)
	// 		cleanupErr = fmt.Errorf("timeout waiting for pipeline cleanup to complete")
	// 	}
	// 	if cleanupErr != nil {
	// 		// return fmt.Errorf("failed to cleanup pipeline: %w", cleanupErr)
	// 	} else {
	// 	}
	// }

	time.Sleep(100 * time.Millisecond) // give some time to settle
	p.log.Debugw("Pipeline closed")

	return nil
}

func (p *BasePipeline) Closed() bool {
	return p.closed.IsBroken()
}

func New(ctx context.Context, log logger.Logger, cleanup func() error) (*BasePipeline, error) {
	pipeline, err := gst.NewPipeline("")
	if err != nil {
		return nil, fmt.Errorf("failed to create gst pipeline: %w", err)
	}

	gp := &BasePipeline{
		log:      log,
		pipeline: pipeline,
		cleanup:  cleanup,
		loop:     event.NewEventLoop(ctx, log),
	}

	go gp.Loop().Run()

	return gp, nil
}

func AddChain[C GstChain](p GstPipeline, chain C) (C, error) {
	var zero C

	p.Log().Debugw("Adding chain to pipeline")
	if err := chain.Create(); err != nil {
		return zero, fmt.Errorf("failed to create chain: %w", err)
	}

	p.Log().Debugw("Adding chain elements to pipeline")
	if err := chain.Add(); err != nil {
		return zero, fmt.Errorf("failed to add chain to pipeline: %w", err)
	}

	p.Log().Debugw("Chain added to pipeline")
	return chain, nil
}

func LinkChains(p GstPipeline, chains ...GstChain) error {
	for i, chain := range chains {
		p.Log().Debugw("Linking chain in pipeline", "chain_index", i)
		if err := chain.Link(); err != nil {
			typ := reflect.TypeOf(chain)
			p.Log().Errorw("Failed to link chain in pipeline", err, "index", i, "chain_type", typ.String())
			return fmt.Errorf("failed to link chain %s in pipeline: %w", typ.String(), err)
		}
	}
	return nil
}

func SyncElements(elements ...*gst.Element) error {
	for _, elem := range elements {
		if !elem.SyncStateWithParent() {
			return fmt.Errorf("failed to sync state for %s", elem.GetName())
		}
	}
	return nil
}
