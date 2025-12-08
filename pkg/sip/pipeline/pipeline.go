package pipeline

import (
	"fmt"
	"sync"
	"time"

	"github.com/frostbyte73/core"
	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/logger"
)

type BasePipeline struct {
	Log      logger.Logger
	Mu       sync.Mutex
	Pipeline *gst.Pipeline
	closed   core.Fuse
}

type GspPipeline interface {
	SetState(state gst.State) error
	SetStateWait(state gst.State) error
	Close() error
	Closed() bool
}

type GstChain interface {
	Add(p *gst.Pipeline) error
	Link(p *gst.Pipeline) error
	Close(p *gst.Pipeline) error
}

func (p *BasePipeline) SetState(state gst.State) error {
	p.Mu.Lock()
	defer p.Mu.Unlock()

	if p.Closed() {
		return fmt.Errorf("cannot set state on closed pipeline")
	}

	if state == gst.StateNull {
		return p.close()
	}

	if err := p.Pipeline.SetState(state); err != nil {
		return fmt.Errorf("failed to set pipeline state: %w", err)
	}

	return nil
}

func (p *BasePipeline) SetStateWait(state gst.State) error {
	p.Mu.Lock()
	defer p.Mu.Unlock()

	if p.Closed() {
		return fmt.Errorf("cannot set state on closed pipeline")
	}

	if state == gst.StateNull {
		return p.close()
	}

	if err := p.Pipeline.SetState(state); err != nil {
		return fmt.Errorf("failed to set pipeline state: %w", err)
	}

	cr, s := p.Pipeline.GetState(state, gst.ClockTime(time.Second*30))
	if cr != gst.StateChangeSuccess {
		return fmt.Errorf("failed to change pipeline state, wanted %s got %s: %s", state.String(), s.String(), cr.String())
	}
	if s != state {
		return fmt.Errorf("pipeline did not reach desired state, wanted %s got %s", state.String(), s.String())
	}

	return nil
}

func (p *BasePipeline) close() error {
	if p.Closed() {
		return nil
	}
	p.closed.Break()

	return p.Pipeline.SetState(gst.StateNull)
}

func (p *BasePipeline) Close() error {
	p.Mu.Lock()
	defer p.Mu.Unlock()

	return p.close()
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
		Log:      log,
		Pipeline: pipeline,
	}

	return gp, nil
}

func (p *BasePipeline) AddChain(chain GstChain, err error) (GstChain, error) {
	if err != nil {
		return nil, fmt.Errorf("failed to build chain: %w", err)
	}

	if err := chain.Add(p.Pipeline); err != nil {
		return nil, fmt.Errorf("failed to add chain to pipeline: %w", err)
	}

	if err := chain.Link(p.Pipeline); err != nil {
		return nil, fmt.Errorf("failed to link chain in pipeline: %w", err)
	}

	return chain, nil
}
