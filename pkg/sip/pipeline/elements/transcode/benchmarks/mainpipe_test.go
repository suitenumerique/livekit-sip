package benchmarks

// Main-process pipeline construction.
//
// The main pipeline has two disjoint chains sharing a single
// GstPipeline:
//
//   out: source -> [cudadownload] -> unixfdsink_out(sockIn)
//   in:  unixfdsrc_in(sockOut)  -> queue -> fakesink
//
// unixfdsrc_in is locked-state (stays NULL while the rest of the
// pipeline goes PAUSED) so its start() vfunc does not try to connect
// to sockOut before the child has bound it. The runner unlocks it
// after the child signals "ready".
//
// Latency measurement has moved to the child process via pure-C pad
// probes (see cprobe.c / cprobe.go). The runner side only retains a
// lightweight one-shot probe on the return-path IPC source to detect
// when the first buffer arrives back — used to start the CPU sampler
// and to time the dot-graph dump.

import (
	"fmt"
	"sync"
	"time"

	"github.com/go-gst/go-gst/gst"
)

// mainPipeline holds the assembled main-side pipeline plus the
// handles the runner needs to drive state transitions and read
// measurements. srcMem and sinkMem are the memory types that
// determined which IPC element family was chosen (MemSystem =
// unixfd, MemCUDA = cudaipc). They are passed to the child via env
// vars so the child picks a matching pair.
//
// Both IPC families: sink=server (binds), src=client (connects).
// IPC src elements are locked in NULL state until their peer server
// is ready:
//   - readyClients (unixfdsrc): unlock after child "ready"
//   - cudaReturnSrc (cudaipcsrc): unlock after child "sink-has-data"
//
// sinkHasDataCh is non-nil when main owns a cudaipcsink (srcMem ==
// MemCUDA). The channel is closed by a one-shot pad probe when the
// first buffer reaches the cudaipcsink server, signalling Run() to
// send "sink-has-data" to the child so it can unlock its cudaipcsrc.
type mainPipeline struct {
	pipeline      *gst.Pipeline
	srcMem        int
	sinkMem       int
	readyClients  []*gst.Element // unlock after "ready" (unixfdsrc)
	cudaReturnSrc *gst.Element   // unlock after child "sink-has-data" (cudaipcsrc)
	sinkHasDataCh <-chan struct{} // closed when main's cudaipcsink gets first buffer
	fwdEOSCh      <-chan struct{} // closed when EOS reaches the forward-path IPC sink

	// firstExitCh is closed by a one-shot probe on the return-path
	// IPC source when the first buffer arrives back from the child.
	// Used to start the CPU sampler and to delay the _playing dot
	// dump until caps have propagated.
	firstExitCh chan struct{}
}

// buildMainPipeline assembles the producer + receiver chains into one
// GstPipeline, installs a one-shot first-exit probe, and sets the
// return-path IPC source to locked state. It picks unixfd or cudaipc
// per direction based on the memory types declared by the element's
// BuildSource and BuildSink. It does NOT change the pipeline state.
func buildMainPipeline(elem Element, cfg Config, sockIn, sockOut string) (*mainPipeline, error) {
	pipeline, err := gst.NewPipeline(fmt.Sprintf("bench-%s-%dx%d-%dx%d",
		elem.Name(), cfg.SourceWidth, cfg.SourceHeight, cfg.TargetWidth, cfg.TargetHeight))
	if err != nil {
		return nil, fmt.Errorf("pipeline: %w", err)
	}

	// ---- Out chain: BuildSource -> ipc-out ----
	srcPad, srcMem, err := elem.BuildSource(pipeline, cfg.SourceWidth, cfg.SourceHeight, cfg.SourceFPS, cfg.NumBuffers)
	if err != nil {
		return nil, fmt.Errorf("BuildSource: %w", err)
	}
	sinkOut, err := ipcSinkFor(srcMem, sockIn)
	if err != nil {
		return nil, fmt.Errorf("ipc sink (out chain): %w", err)
	}
	// sync=false: forward as fast as upstream produces.
	// async=false: state transition completes inline so SetState
	// blocks until bind/listen is done, and we can reason about
	// "PAUSED reached" without a separate GetState wait.
	sinkOut.SetProperty("sync", false)
	sinkOut.SetProperty("async", false)
	if err := pipeline.Add(sinkOut); err != nil {
		return nil, fmt.Errorf("add ipc sink (out chain): %w", err)
	}
	if ret := srcPad.Link(sinkOut.GetStaticPad("sink")); ret != gst.PadLinkOK {
		return nil, fmt.Errorf("link producer -> ipc sink: %s", ret)
	}
	// sinkOut is always a server (binds) — never locked.
	// If it is a cudaipcsink, install a one-shot probe so Run()
	// knows when the first buffer has reached the server and can
	// tell the child to unlock its cudaipcsrc client.
	var sinkHasDataCh <-chan struct{}
	if srcMem == MemCUDA {
		sinkHasDataCh = installSinkHasDataProbe(sinkOut)
	}

	// Detect EOS arriving at the forward-path IPC sink. cudaipcsink
	// (mmap mode) does NOT forward EOS to the client via the IPC
	// protocol — it only sends EOS during stop() (state→NULL). In
	// our 2-chain pipeline that creates a deadlock. The runner uses
	// fwdEOSCh to detect that the source has finished and sends an
	// explicit "forward-eos" IPC message to the child.
	fwdEOSCh := installEOSProbe(sinkOut)

	// ---- In chain: ipc-in -> BuildSink chain ----
	sinkChainPad, sinkMem, err := elem.BuildSink(pipeline)
	if err != nil {
		return nil, fmt.Errorf("BuildSink: %w", err)
	}
	srcIn, err := ipcSrcFor(sinkMem, sockOut)
	if err != nil {
		return nil, fmt.Errorf("ipc src (in chain): %w", err)
	}
	if err := pipeline.Add(srcIn); err != nil {
		return nil, fmt.Errorf("add ipc src (in chain): %w", err)
	}
	if ret := srcIn.GetStaticPad("src").Link(sinkChainPad); ret != gst.PadLinkOK {
		return nil, fmt.Errorf("link ipc src -> sink chain: %s", ret)
	}
	// srcIn is always a client (connects). Lock it so its start()
	// does not try to connect before the child's server peer is
	// ready. Unlock timing differs by memory type:
	//   MemSystem (unixfdsrc): unlock after child "ready"
	//   MemCUDA (cudaipcsrc):  unlock after child "sink-has-data"
	var readyClients []*gst.Element
	var cudaReturnSrc *gst.Element
	if err := srcIn.SetLockedState(true); err != nil {
		return nil, fmt.Errorf("lock srcIn: %w", err)
	}
	switch sinkMem {
	case MemSystem:
		readyClients = append(readyClients, srcIn)
	case MemCUDA:
		cudaReturnSrc = srcIn
	}

	// ---- First-exit one-shot probe ----
	// Closes firstExitCh when the first buffer arrives on the
	// return path. Used to start the CPU sampler and delay the
	// _playing dot dump. Self-removes after firing.
	firstExitCh := make(chan struct{})
	var firstExitOnce sync.Once
	exitPad := srcIn.GetStaticPad("src")
	if exitPad == nil {
		return nil, fmt.Errorf("ipc src (in) src pad missing")
	}
	exitPad.AddProbe(gst.PadProbeTypeBuffer|gst.PadProbeTypeBufferList,
		func(_ *gst.Pad, _ *gst.PadProbeInfo) gst.PadProbeReturn {
			firstExitOnce.Do(func() { close(firstExitCh) })
			return gst.PadProbeRemove
		})

	return &mainPipeline{
		pipeline:      pipeline,
		srcMem:        srcMem,
		sinkMem:       sinkMem,
		readyClients:  readyClients,
		cudaReturnSrc: cudaReturnSrc,
		sinkHasDataCh: sinkHasDataCh,
		fwdEOSCh:      fwdEOSCh,
		firstExitCh:   firstExitCh,
	}, nil
}

// unlockReadyClients flips unixfdsrc IPC clients out of NULL and
// syncs them to the pipeline's current state. Must be called AFTER
// the child has signalled "ready" (so the child's unixfdsink servers
// are bound). Does NOT touch cudaReturnSrc — that is unlocked
// separately after the child sends "sink-has-data".
func (m *mainPipeline) unlockReadyClients() error {
	for _, e := range m.readyClients {
		if err := unlockAndSync(e); err != nil {
			return err
		}
	}
	return nil
}

// unlockCudaReturnSrc flips the return-path cudaipcsrc out of NULL.
// Must be called AFTER the child has signalled "sink-has-data" (so
// the child's cudaipcsink server has received its first buffer and
// the CONFIG message will be sent when we connect).
func (m *mainPipeline) unlockCudaReturnSrc() error {
	if m.cudaReturnSrc == nil {
		return nil
	}
	return unlockAndSync(m.cudaReturnSrc)
}

// unlockAndSync unlocks a single element, syncs it to its parent's
// state, and waits for the transition to complete.
func unlockAndSync(e *gst.Element) error {
	if err := e.SetLockedState(false); err != nil {
		return fmt.Errorf("unlock %s: %w", e.GetName(), err)
	}
	if ok := e.SyncStateWithParent(); !ok {
		return fmt.Errorf("%s SyncStateWithParent returned false", e.GetName())
	}
	cr, _ := e.GetState(gst.StatePaused, gst.ClockTime(10*time.Second))
	if cr != gst.StateChangeSuccess && cr != gst.StateChangeNoPreroll {
		return fmt.Errorf("%s did not reach PAUSED after unlock: %s", e.GetName(), cr.String())
	}
	return nil
}
