package pipeline

import (
	"fmt"
	"os"
	"reflect"
	"time"

	"github.com/frostbyte73/core"
	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/logger"
)

type BasePipeline struct {
	log      logger.Logger
	pipeline *gst.Pipeline
	closed   core.Fuse
}

type GstPipeline interface {
	Pipeline() *gst.Pipeline
	Log() logger.Logger
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

	p.log.Debugw("Setting pipeline to null state", "pid", pid)
	err := p.Pipeline().SetState(gst.StateNull)
	p.log.Debugw("Pipeline set to null state", "err", err)
	time.Sleep(100 * time.Millisecond) // give some time to settle
	if err != nil {
		p.log.Debugw("Failed to set pipeline to null state", "err", err)
		return fmt.Errorf("failed to set pipeline to null state: %w", err)
	}

	p.log.Debugw("Pipeline closed")

	return nil
}

func (p *BasePipeline) Closed() bool {
	return p.closed.IsBroken()
}

func New(log logger.Logger) (*BasePipeline, error) {
	pipeline, err := gst.NewPipeline("")
	if err != nil {
		return nil, fmt.Errorf("failed to create gst pipeline: %w", err)
	}

	gp := &BasePipeline{
		log:      log,
		pipeline: pipeline,
	}

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

	// p.Log().Debugw("Linking chain in pipeline")
	// if err := chain.Link(); err != nil {
	// 	return zero, fmt.Errorf("failed to link chain in pipeline: %w", err)
	// }

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
