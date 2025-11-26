package pipeline

import (
	"fmt"
	"sync"
	"time"

	"github.com/frostbyte73/core"
	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/logger"
)

type GstPipelineChain = []*gst.Element

type GstPipeline struct {
	log      logger.Logger
	mu       sync.Mutex
	closed   core.Fuse
	Pipeline *gst.Pipeline

	*SipToWebrtc
	*SelectorToSip
}

type GstChain interface {
	Add(pipeline *gst.Pipeline) error
	Link(pipeline *gst.Pipeline) error
	Close(pipeline *gst.Pipeline) error
}

func (gp *GstPipeline) SetState(state gst.State) error {
	gp.mu.Lock()
	defer gp.mu.Unlock()

	if gp.Closed() {
		return fmt.Errorf("cannot set state on closed pipeline")
	}

	if state == gst.StateNull {
		return gp.close()
	}

	if err := gp.Pipeline.SetState(state); err != nil {
		return fmt.Errorf("failed to set pipeline state: %w", err)
	}

	return nil
}

func (gp *GstPipeline) SetStateWait(state gst.State) error {
	gp.mu.Lock()
	defer gp.mu.Unlock()

	if gp.Closed() {
		return fmt.Errorf("cannot set state on closed pipeline")
	}

	if state == gst.StateNull {
		return gp.close()
	}

	if err := gp.Pipeline.SetState(state); err != nil {
		return fmt.Errorf("failed to set pipeline state: %w", err)
	}

	cr, s := gp.Pipeline.GetState(state, gst.ClockTime(time.Second*30))
	if cr != gst.StateChangeSuccess {
		return fmt.Errorf("failed to change pipeline state, wanted %s got %s: %s", state.String(), s.String(), cr.String())
	}
	if s != state {
		return fmt.Errorf("pipeline did not reach desired state, wanted %s got %s", state.String(), s.String())
	}

	return nil
}

func (gp *GstPipeline) close() error {
	if gp.Closed() {
		return nil
	}
	gp.closed.Break()

	return gp.Pipeline.SetState(gst.StateNull)
}

func (gp *GstPipeline) Close() error {
	gp.mu.Lock()
	defer gp.mu.Unlock()

	return gp.close()
}

func (gp *GstPipeline) Closed() bool {
	return gp.closed.IsBroken()
}

func NewGstPipeline(log logger.Logger, sipInPayload, sipOutPayload int) (*GstPipeline, error) {
	pipeline, err := gst.NewPipeline("")
	if err != nil {
		return nil, fmt.Errorf("failed to create gst pipeline: %w", err)
	}

	gp := &GstPipeline{
		log:      log,
		Pipeline: pipeline,
	}

	gp.SipToWebrtc, err = CastErr[*SipToWebrtc](gp.addChain(buildSipToWebRTCChain(log.WithComponent("sip_to_webrtc"), sipInPayload)))
	if err != nil {
		return nil, err
	}

	gp.SelectorToSip, err = CastErr[*SelectorToSip](gp.addChain(buildSelectorToSipChain(log.WithComponent("selector_to_sip"), sipOutPayload)))
	if err != nil {
		return nil, err
	}

	return gp, nil
}

func (gp *GstPipeline) addChain(chain GstChain, err error) (GstChain, error) {
	if err != nil {
		return nil, fmt.Errorf("failed to build chain: %w", err)
	}

	if err := chain.Add(gp.Pipeline); err != nil {
		return nil, fmt.Errorf("failed to add chain to pipeline: %w", err)
	}

	if err := chain.Link(gp.Pipeline); err != nil {
		return nil, fmt.Errorf("failed to link chain in pipeline: %w", err)
	}

	return chain, nil
}
