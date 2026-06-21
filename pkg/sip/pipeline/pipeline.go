package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sync/atomic"
	"time"

	"github.com/frostbyte73/core"
	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/sip/pkg/config"
	"github.com/livekit/sip/pkg/sip/pipeline/debug"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/sipbin"
)

type CallStats struct {
	Microphone        *sipbin.RTPSessionStats
	MicrophonePtCaps  map[int]string
	Camera            *sipbin.RTPSessionStats
	CameraPtCaps      map[int]string
	ScreenShare       *sipbin.RTPSessionStats
	ScreenSharePtCaps map[int]string
}

type Pipeline struct {
	Log       logger.Logger
	pipeline  *gst.Pipeline
	ctx       context.Context
	cancel    context.CancelFunc
	sipCallID string
	closed    core.Fuse
	cleanup   func() error
	bus       *gst.Bus
	dtmfCh    chan int

	dumpCH   chan bool
	debugSrv *debug.Server

	videoWidth            uint
	videoHeight           uint
	videoFramerate        uint
	lang                  string
	maxActiveParticipants int
	dumpDot               bool
	dumpDir               string
	publishCoders         config.PublishCodecConfig

	stats atomic.Pointer[CallStats]

	*SipIo
	*WebrtcIo
	// *SipToWebrtc
	// *WebrtcToSip
	*IOManager
}

type GstChain interface {
	Create() error
	Add() error
	Link() error
	Close() error
}

func (p *Pipeline) Pipeline() *gst.Pipeline {
	return p.pipeline
}

func (p *Pipeline) SetState(state gst.State) error {
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

func (p *Pipeline) SetStateWait(state gst.State) error {
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

func (p *Pipeline) GetStats() (*CallStats, error) {
	if p.Closed() {
		return p.stats.Load(), nil
	}
	stats, err := p.getStats()
	if err != nil {
		return nil, err
	}
	return stats, nil
}

func (p *Pipeline) getStats() (*CallStats, error) {
	structureVal, err := p.SipIo.SipBin.Emit("stats")
	if err != nil {
		p.Log.Warnw("Failed to emit stats signal", err)
		return nil, err
	}
	if structureVal == nil {
		return nil, fmt.Errorf("received nil stats structure")
	}

	structure, ok := structureVal.(*gst.Structure)
	if !ok {
		p.Log.Warnw("Failed to convert stats signal result to GstStructure", nil, "value", fmt.Sprintf("%T=%v", structureVal, structureVal))
		return nil, fmt.Errorf("failed to convert stats signal result to GstStructure")
	}

	stats := &CallStats{}
	for _, stats := range []struct {
		kind   livekit.TrackSource
		stats  **sipbin.RTPSessionStats
		ptCaps *map[int]string
	}{
		{livekit.TrackSource_MICROPHONE, &stats.Microphone, &stats.MicrophonePtCaps},
		{livekit.TrackSource_CAMERA, &stats.Camera, &stats.CameraPtCaps},
		{livekit.TrackSource_SCREEN_SHARE, &stats.ScreenShare, &stats.ScreenSharePtCaps},
	} {
		statsVal, err := structure.GetValue(stats.kind.String())
		if err != nil || statsVal == nil {
			continue
		}
		av, ok := statsVal.(glib.ArbitraryValue)
		if !ok {
			p.Log.Warnw("Expected ArbitraryValue", nil, stats.kind.String(), fmt.Sprintf("%T=%v", statsVal, statsVal))
			continue
		}
		sessionStats, ok := av.Data.(*sipbin.RTPSessionStats)
		if !ok {
			p.Log.Warnw("Wrong inner type", nil, stats.kind.String(), fmt.Sprintf("%T", av.Data))
			continue
		}
		if sessionStats != nil {
			*stats.stats = sessionStats
		}
		*stats.ptCaps = make(map[int]string)
		capsVal, err := structure.GetValue(fmt.Sprintf("%s-caps", stats.kind.String()))
		if err != nil || capsVal == nil {
			continue
		}
		caps, ok := capsVal.(*gst.Caps)
		if !ok {
			p.Log.Warnw("Expected GstCaps", nil, stats.kind.String(), fmt.Sprintf("%T=%v", capsVal, capsVal))
			continue
		}
		for i := range caps.GetSize() {
			st := caps.GetStructureAt(i)
			pt, err := st.GetInt("payload")
			if err != nil {
				p.Log.Warnw("Failed to get payload type from caps structure", err, stats.kind.String(), "structure_index", i)
				continue
			}
			(*stats.ptCaps)[pt] = st.String()
			st = nil
		}
		caps = nil
	}

	p.Log.Debugw("Received call stats update", "stats", stats)
	p.stats.Store(stats)
	return stats, nil
}

var pid = os.Getpid()

func (p *Pipeline) Close() error {
	if !p.closed.Break() {
		return nil
	}
	p.Log.Debugw("Closing pipeline")

	p.getStats()

	p.cancel()

	done := make(chan struct{})
	var err error
	go func() {
		defer close(done)

		p.SipIo.SipBin.SetLockedState(true)
		p.WebrtcIo.LivekitBin.SetLockedState(true)
		p.IOManager.SipController.SetLockedState(true)
		p.IOManager.LivekitController.SetLockedState(true)

		p.Log.Debugw("Pipline SetState NULL")
		err = errors.Join(err, p.Pipeline().SetState(gst.StateNull))
		p.Log.Debugw("Pipline SetState NULL complete")

		p.Log.Debugw("SipBin SetState NULL")
		err = errors.Join(err, p.SipIo.SipBin.SetState(gst.StateNull))
		p.Log.Debugw("SipBin SetState NULL complete")

		p.Log.Debugw("LivekitController SetState NULL")
		err = errors.Join(err, p.IOManager.LivekitController.SetState(gst.StateNull))
		p.Log.Debugw("LivekitController SetState NULL complete")

		p.Log.Debugw("LivekitBin SetState NULL")
		err = errors.Join(err, p.WebrtcIo.LivekitBin.SetState(gst.StateNull))
		p.Log.Debugw("LivekitBin SetState NULL complete")

		p.Log.Debugw("SipController SetState NULL")
		err = errors.Join(err, p.IOManager.SipController.SetState(gst.StateNull))
		p.Log.Debugw("SipController SetState NULL complete")

		p.SipIo.SipBin.SetLockedState(false)
		p.WebrtcIo.LivekitBin.SetLockedState(false)
		p.IOManager.SipController.SetLockedState(false)
		p.IOManager.LivekitController.SetLockedState(false)

		p.Log.Debugw("Pipeline set to null state complete", "pid", pid, "err", err)
	}()

	closed := false
	select {
	case <-done:
		closed = true
	case <-time.After(10 * time.Second):
	}
	if !closed {
		p.Log.Warnw("Timeout waiting for pipeline to set to null state, sending flush event", nil)
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
		p.Log.Warnw("Timeout waiting for pipeline to set to null state after flush start, sending flush stop event", nil)
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
		p.Log.Warnw("Timeout waiting for pipeline to set to null state after flush stop, trying to break clock", nil)
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
		p.Log.Warnw("Failed to set pipeline to null state after breaking clock, trying early cleanup", nil)
		if err := p.cleanup(); err != nil {
			p.Log.Errorw("Failed timeout cleanup before setting pipeline to null state", err)
		}
		p.cleanup = nil // prevent double cleanup
		select {
		case <-done:
			closed = true
		case <-time.After(5 * time.Second):
		}
	}

	if !closed {
		p.Log.Errorw("Failed to set pipeline to null state after breaking clock", errors.New("timeout waiting for null state"))
		return fmt.Errorf("failed to set pipeline to null state")
	}

	p.Log.Debugw("Pipeline set to null state")

	if p.cleanup != nil {
		p.Log.Debugw("Running pipeline cleanup")
		if err := p.cleanup(); err != nil {
			p.Log.Errorw("Failed timeout cleanup before setting pipeline to null state", err)
		}
		p.Log.Debugw("Pipeline cleanup complete")
	}

	if p.debugSrv != nil {
		p.debugSrv.Stop(context.Background())
	}

	p.CloseBus()
	p.Log.Debugw("Pipeline bus closed")

	time.Sleep(100 * time.Millisecond) // give some time to settle
	p.Log.Infow("Pipeline closed")

	p.pipeline = nil

	return nil
}

func (p *Pipeline) Closed() bool {
	return p.closed.IsBroken()
}

func New(ctx context.Context, log logger.Logger, sipOpt SipOpt, sipCallID string) (*Pipeline, error) {
	log.Debugw("Creating pipeline")
	pipeline, err := gst.NewPipeline("")
	if err != nil {
		return nil, fmt.Errorf("failed to create gst pipeline: %w", err)
	}

	ctx, cancel := context.WithCancel(ctx)

	p := &Pipeline{
		Log:                   log.WithComponent("pipeline"),
		pipeline:              pipeline,
		ctx:                   ctx,
		cancel:                cancel,
		dtmfCh:                make(chan int, 10),
		dumpCH:                make(chan bool, 1024),
		videoWidth:            sipOpt.VideoWidth,
		videoHeight:           sipOpt.VideoHeight,
		videoFramerate:        sipOpt.Framerate,
		lang:                  sipOpt.Lang,
		maxActiveParticipants: sipOpt.MaxActiveParticipants,
		sipCallID:             sipCallID,
		dumpDot:               sipOpt.Gst.DumpDot,
		dumpDir:               sipOpt.Gst.DumpDir,
		publishCoders:         sipOpt.PublishCodecs,
	}
	p.cleanup = p.cleanupChains

	go func() {
		<-p.ctx.Done()
		p.Close()
	}()

	p.SetLogHandler()

	p.Log.Debugw("Setting up bus")
	p.SetupBus()

	p.debugSrv = debug.NewServer(":8888", p.dumpCH)
	if err := p.debugSrv.Start(); err != nil {
		p.Log.Warnw("Failed to start debug server", err)
	}
	p.pipeline.Connect("deep-element-added", func(_ any, _ any, child *gst.Element) {
		p.debugSrv.OnElementAdded(child)
	})
	p.pipeline.Connect("deep-element-removed", func(_ any, _ any, child *gst.Element) {
		p.debugSrv.OnElementRemoved(child)
	})

	p.Log.Debugw("Adding SIP IO chain")
	p.SipIo, err = AddChain(p, NewSipInput(log, p, sipOpt))
	if err != nil {
		p.Log.Errorw("Failed to add SIP IO chain", err)
		return nil, err
	}

	p.Log.Debugw("Adding Webrtc IO chain")
	p.WebrtcIo, err = AddChain(p, NewWebrtcIo(log, p))
	if err != nil {
		p.Log.Errorw("Failed to add WebRTC IO chain", err)
		return nil, err
	}

	p.Log.Debugw("Adding IO chain")
	p.IOManager, err = AddChain(p, NewIOChain(log, p))
	if err != nil {
		p.Log.Errorw("Failed to add IO chain", err)
		return nil, err
	}

	// p.Log.Debugw("Adding WebRTC to SIP chain")
	// p.WebrtcToSip, err = AddChain(p, NewWebrtcToSipChain(log, p))
	// if err != nil {
	// 	p.Log.Errorw("Failed to add WebRTC to SIP chain", err)
	// 	return nil, err
	// }

	p.Log.Debugw("Linking chains")
	if err := LinkChains(p,
		p.SipIo,
		p.WebrtcIo,
		// p.SipToWebrtc,
		// p.WebrtcToSip,
		p.IOManager,
	); err != nil {
		p.Log.Errorw("Failed to link chains", err)
		return nil, err
	}

	p.Log.Debugw("Pipeline created")

	return p, nil
}

func (p *Pipeline) cleanupChains() error {
	p.Log.Debugw("Closing pipeline chains")

	p.Log.Debugw("Closing SIP IO")
	if p.SipIo != nil {
		if err := p.SipIo.Close(); err != nil {
			return fmt.Errorf("failed to close SIP IO: %w", err)
		}
		p.SipIo = nil
	}

	p.Log.Debugw("Closing WebRTC IO")
	if p.WebrtcIo != nil {
		if err := p.WebrtcIo.Close(); err != nil {
			return fmt.Errorf("failed to close WebRTC IO: %w", err)
		}
		p.WebrtcIo = nil
	}

	p.Log.Debugw("Closing IO chain")
	if p.IOManager != nil {
		if err := p.IOManager.Close(); err != nil {
			return fmt.Errorf("failed to close IO chain: %w", err)
		}
		p.IOManager = nil
	}

	p.Log.Debugw("Pipeline chains closed")
	return nil
}

func AddChain[C GstChain](p *Pipeline, chain C) (C, error) {
	var zero C

	p.Log.Debugw("Adding chain to pipeline")
	if err := chain.Create(); err != nil {
		return zero, fmt.Errorf("failed to create chain: %w", err)
	}

	p.Log.Debugw("Adding chain elements to pipeline")
	if err := chain.Add(); err != nil {
		return zero, fmt.Errorf("failed to add chain to pipeline: %w", err)
	}

	p.Log.Debugw("Chain added to pipeline")
	return chain, nil
}

func LinkChains(p *Pipeline, chains ...GstChain) error {
	for i, chain := range chains {
		p.Log.Debugw("Linking chain in pipeline", "chain_index", i)
		if err := chain.Link(); err != nil {
			typ := reflect.TypeOf(chain)
			p.Log.Errorw("Failed to link chain in pipeline", err, "index", i, "chain_type", typ.String())
			return fmt.Errorf("failed to link chain %s in pipeline: %w", typ.String(), err)
		}
	}
	return nil
}
