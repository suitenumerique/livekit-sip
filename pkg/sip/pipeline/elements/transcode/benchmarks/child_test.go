package benchmarks

// Child-process half of the split-process benchmark runner. The test
// binary re-execs itself via /proc/self/exe with GSTBENCH_ROLE=1,
// which causes TestMain to hand off to runChild before ever entering
// the testing framework.
//
// The child builds a pipeline containing only the element-under-test
// (plus unixfdsrc/unixfdsink adapters), installs pure-C latency pad
// probes on the EUT's ghost pads (see cprobe.c), runs the pipeline,
// writes the latency data to a binary file, signals state transitions
// over fd 3, and exits.
//
// Measurement split:
//   * EUT latency    — C pad probes in this process (no CGo per buffer)
//   * CPU load       — /proc/<child-pid>/stat from the runner side
//   * per-element    — GST latency tracer (configured via env by runner)
//
// IPC with main uses two newline-delimited pipes:
//
//   fd 3 (child → main):
//     "children:a,b,c" -> list of EUT child element names
//     "ready"          -> reached PAUSED, sockets are up
//     "playing"        -> reached PLAYING
//     "sink-has-data"  -> child's cudaipcsink got first buffer (CUDA return path only)
//     "eos"            -> observed EOS on its bus
//     "done"           -> about to exit cleanly
//     "error: X"       -> runChildImpl returned an error
//
//   fd 4 (main → child):
//     "sink-has-data"  -> main's cudaipcsink got first buffer (CUDA forward path only)
//
// The "sink-has-data" handshake ensures cudaipcsrc (client) does not
// connect until the peer cudaipcsink (server) has received its first
// buffer. See installSinkHasDataProbe in ipc_test.go.
//
// See childproc_test.go for the receiver side of this protocol.
//
// The latency tracer runs in this process via env vars (GST_TRACERS,
// GST_DEBUG, GST_DEBUG_FILE) set by main at spawn time, NOT by the
// child itself. The child never reads its own trace log — main does
// that after cmd.Wait returns.

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-gst/go-gst/gst"
)

// waitFromMain blocks on the main→child channel until it receives the
// expected message, or returns an error on timeout / pipe close.
func waitFromMain(fromMain <-chan string, want string, d time.Duration) error {
	deadline := time.After(d)
	for {
		select {
		case msg, ok := <-fromMain:
			if !ok {
				return fmt.Errorf("main pipe closed before %q", want)
			}
			if msg == want {
				return nil
			}
			// Unexpected message — log to stderr and keep waiting.
			fmt.Fprintf(os.Stderr, "gstbench child: unexpected %q from main while waiting for %q\n", msg, want)
		case <-deadline:
			return fmt.Errorf("timeout waiting for main %q after %v", want, d)
		}
	}
}

// childEnv collects the inputs runChildImpl needs from GSTBENCH_*
// environment variables. Centralising the parsing keeps the main body
// of runChildImpl readable.
type childEnv struct {
	elemName string
	sockIn   string
	sockOut  string
	targetW  int
	targetH  int
	// Source dimensions are passed through only so dot filenames match
	// the parent's naming scheme; the child pipeline never sees the
	// raw source.
	sourceW int
	sourceH int
	dotDir  string
	// srcMem / sinkMem must match the values main used when picking
	// ipcSinkFor / ipcSrcFor for the two sockets — otherwise the
	// peers speak different IPC protocols and handshake fails.
	srcMem  int
	sinkMem int
	// numBuffers is the number of buffers the source will produce.
	// Used to pre-allocate the C probe arrays.
	numBuffers int
	// cprobePath is the temp file where the child writes its C probe
	// latency data after EOS, for the runner to read post-exit.
	cprobePath string
}

func parseChildEnv() (*childEnv, error) {
	e := &childEnv{
		elemName: os.Getenv("GSTBENCH_ELEMENT"),
		sockIn:   os.Getenv("GSTBENCH_SOCK_IN"),
		sockOut:  os.Getenv("GSTBENCH_SOCK_OUT"),
		dotDir:   os.Getenv("GSTBENCH_DOT_DIR"),
	}
	if e.elemName == "" || e.sockIn == "" || e.sockOut == "" {
		return nil, fmt.Errorf("missing required env (GSTBENCH_ELEMENT/SOCK_IN/SOCK_OUT)")
	}
	var err error
	if e.targetW, err = strconv.Atoi(os.Getenv("GSTBENCH_TARGET_W")); err != nil {
		return nil, fmt.Errorf("GSTBENCH_TARGET_W: %w", err)
	}
	if e.targetH, err = strconv.Atoi(os.Getenv("GSTBENCH_TARGET_H")); err != nil {
		return nil, fmt.Errorf("GSTBENCH_TARGET_H: %w", err)
	}
	e.sourceW, _ = strconv.Atoi(os.Getenv("GSTBENCH_SOURCE_W"))
	e.sourceH, _ = strconv.Atoi(os.Getenv("GSTBENCH_SOURCE_H"))
	if e.numBuffers, err = strconv.Atoi(os.Getenv("GSTBENCH_NUM_BUFFERS")); err != nil {
		return nil, fmt.Errorf("GSTBENCH_NUM_BUFFERS: %w", err)
	}
	e.cprobePath = os.Getenv("GSTBENCH_CPROBE_PATH")
	// srcMem/sinkMem are sent by the runner via IPC ("mem:src,sink")
	// after it builds its pipeline and knows the memory types. They
	// are set later by runChildImpl before buildChildPipeline.
	return e, nil
}

// runChild is invoked from TestMain when GSTBENCH_ROLE=1. It never
// returns — it either os.Exit(0)s after the EUT's EOS, or os.Exit(1)s
// with a message on stderr and an "error:" line over IPC.
func runChild() {
	// fd 3: child → runner (outbound IPC)
	ipcFile := os.NewFile(3, "gstbench-ipc")
	var ipcW *bufio.Writer
	if ipcFile != nil {
		ipcW = bufio.NewWriter(ipcFile)
	}
	sendIPC := func(msg string) {
		if ipcW == nil {
			return
		}
		_, _ = ipcW.WriteString(msg + "\n")
		_ = ipcW.Flush()
	}
	closeIPC := func() {
		if ipcFile != nil {
			_ = ipcFile.Close()
		}
	}

	// fd 4: main → child (inbound IPC)
	fromMainFile := os.NewFile(4, "gstbench-from-main")
	fromMainCh := make(chan string, 4)
	go func() {
		defer close(fromMainCh)
		if fromMainFile == nil {
			return
		}
		scanner := bufio.NewScanner(fromMainFile)
		for scanner.Scan() {
			fromMainCh <- scanner.Text()
		}
	}()
	closeFromMain := func() {
		if fromMainFile != nil {
			_ = fromMainFile.Close()
		}
	}

	if err := runChildImpl(sendIPC, fromMainCh); err != nil {
		fmt.Fprintf(os.Stderr, "gstbench child: %v\n", err)
		sendIPC("error: " + err.Error())
		closeIPC()
		closeFromMain()
		os.Exit(1)
	}
	sendIPC("done")
	closeIPC()
	closeFromMain()
	os.Exit(0)
}

// runChildImpl is the happy-path body of the child.
func runChildImpl(sendIPC func(string), fromMain <-chan string) error {
	env, err := parseChildEnv()
	if err != nil {
		return err
	}
	elem, err := lookupElement(env.elemName)
	if err != nil {
		return err
	}

	// Wait for the runner to send memory types. The runner determines
	// them from BuildSource/BuildSink after building its own pipeline,
	// then sends "mem:srcMem,sinkMem" over the IPC channel.
	fmt.Fprintf(os.Stderr, "gstbench child[%s]: waiting for mem config from runner\n", env.elemName)
	select {
	case msg, ok := <-fromMain:
		if !ok {
			return fmt.Errorf("runner pipe closed before mem config")
		}
		if !strings.HasPrefix(msg, "mem:") {
			return fmt.Errorf("expected mem:X,Y from runner, got %q", msg)
		}
		parts := strings.Split(strings.TrimPrefix(msg, "mem:"), ",")
		if len(parts) != 2 {
			return fmt.Errorf("malformed mem config: %q", msg)
		}
		env.srcMem, _ = strconv.Atoi(parts[0])
		env.sinkMem, _ = strconv.Atoi(parts[1])
		fmt.Fprintf(os.Stderr, "gstbench child[%s]: mem config srcMem=%d sinkMem=%d\n", env.elemName, env.srcMem, env.sinkMem)
	case <-time.After(10 * time.Second):
		return fmt.Errorf("timeout waiting for mem config from runner")
	}

	pipeline, eutChildren, lockedSrc, sinkHasDataCh, probe, err := buildChildPipeline(elem, env)
	if err != nil {
		return err
	}
	defer probe.free()
	// Tell main which element names belong to the EUT bin so its
	// trace-log parser can filter infrastructure elements
	// (unixfdsrc, unixfdsink, cudaupload/download) out of the
	// per-child latency report.
	sendIPC("children:" + strings.Join(eutChildren, ","))
	dotBase := fmt.Sprintf("%s_%dx%d_to_%dx%d_C", env.elemName, env.sourceW, env.sourceH, env.targetW, env.targetH)
	return runChildPipeline(pipeline, lockedSrc, sinkHasDataCh, probe, env.cprobePath, sendIPC, fromMain, env.dotDir, dotBase, env.elemName)
}

// buildChildPipeline assembles the child's single-chain pipeline:
//
//	ipc-in(srcMem, sockIn) -> EUT -> ipc-out(sinkMem, sockOut)
//
// The IPC element family (unixfd or cudaipc) is chosen by main and
// passed through env vars. There are no adapters here — the IPC
// element carries the correct memory type end-to-end, so the EUT
// sees exactly the memory type it wants with no extra copies.
//
// Returns the pipeline and the ordered list of EUT-bin child element
// names so main can filter the trace log by them.
// buildChildPipeline returns (pipeline, eutChildren, lockedSrc, sinkHasDataCh, err).
// lockedSrc is non-nil when the forward IPC is cudaipc and the
// cudaipcsrc element must stay locked until main sends "sink-has-data".
// sinkHasDataCh is non-nil when the return IPC is cudaipc; it is
// closed by a one-shot probe when the child's cudaipcsink receives
// its first buffer.
func buildChildPipeline(elem Element, env *childEnv) (*gst.Pipeline, []string, *gst.Element, <-chan struct{}, *cProbe, error) {
	fail := func(err error) (*gst.Pipeline, []string, *gst.Element, <-chan struct{}, *cProbe, error) {
		return nil, nil, nil, nil, nil, err
	}

	pipeline, err := gst.NewPipeline("bench-child-" + env.elemName)
	if err != nil {
		return fail(fmt.Errorf("pipeline: %w", err))
	}

	src, err := ipcSrcFor(env.srcMem, env.sockIn)
	if err != nil {
		return fail(fmt.Errorf("ipc src (%s): %w", memTypeName(env.srcMem), err))
	}
	if err := pipeline.Add(src); err != nil {
		return fail(fmt.Errorf("add ipc src: %w", err))
	}

	eut, err := elem.BuildElement(pipeline, env.targetW, env.targetH)
	if err != nil {
		return fail(fmt.Errorf("BuildElement: %w", err))
	}

	sink, err := ipcSinkFor(env.sinkMem, env.sockOut)
	if err != nil {
		return fail(fmt.Errorf("ipc sink (%s): %w", memTypeName(env.sinkMem), err))
	}
	// sync=false: forward as fast as the producer provides.
	// async=false: state transition completes inline.
	sink.SetProperty("sync", false)
	sink.SetProperty("async", false)
	if err := pipeline.Add(sink); err != nil {
		return fail(fmt.Errorf("add ipc sink: %w", err))
	}

	if err := gst.ElementLinkMany(src, eut, sink); err != nil {
		return fail(fmt.Errorf("link ipc-in -> eut -> ipc-out: %w", err))
	}

	// When the forward IPC is cudaipc (srcMem == MemCUDA),
	// cudaipcsrc must NOT connect until main's cudaipcsink has
	// data. Lock it here; runChildPipeline waits for main's
	// "sink-has-data" message before unlocking.
	var locked *gst.Element
	if env.srcMem == MemCUDA {
		if err := src.SetLockedState(true); err != nil {
			return fail(fmt.Errorf("lock cudaipcsrc: %w", err))
		}
		locked = src
	}

	// When the return IPC is cudaipc (sinkMem == MemCUDA), install
	// a one-shot probe so the child can signal main "sink-has-data"
	// once the first buffer reaches the cudaipcsink server.
	var sinkHasDataCh <-chan struct{}
	if env.sinkMem == MemCUDA {
		sinkHasDataCh = installSinkHasDataProbe(sink)
	}

	// Enumerate the EUT bin's children so main can filter its
	// trace-log parse to just the element-under-test, excluding the
	// ipc infrastructure.
	bin := gst.ToGstBin(eut)
	if bin == nil {
		return fail(fmt.Errorf("%s did not cast to GstBin", env.elemName))
	}
	childElems, err := bin.GetElementsRecursive()
	if err != nil {
		return fail(fmt.Errorf("list children: %w", err))
	}
	eutChildren := make([]string, 0, len(childElems))
	for _, c := range childElems {
		eutChildren = append(eutChildren, c.GetName())
	}

	// Install pure-C latency probes on the EUT's ghost pads.
	probe := newCProbe(env.numBuffers)
	eutSinkPad := eut.GetStaticPad("sink")
	if eutSinkPad == nil {
		return fail(fmt.Errorf("EUT sink pad missing"))
	}
	probe.installEntry(eutSinkPad)
	eutSrcPad := eut.GetStaticPad("src")
	if eutSrcPad == nil {
		return fail(fmt.Errorf("EUT src pad missing"))
	}
	probe.installExit(eutSrcPad)

	return pipeline, eutChildren, locked, sinkHasDataCh, probe, nil
}

// runChildPipeline runs the PAUSED -> PLAYING -> bus-loop -> NULL
// cycle, sending IPC status updates to main at each phase. The 90s
// child-side bus deadline is shorter than main's 120s so on a hang
// the child aborts first and main can still cmd.Wait() cleanly.
//
// lockedSrc is the forward-path cudaipcsrc (non-nil when srcMem ==
// MemCUDA). It stays locked until main sends "sink-has-data".
//
// sinkHasDataCh is non-nil when the return IPC is cudaipc. When
// closed (by the one-shot probe on the child's cudaipcsink), the
// child sends "sink-has-data" so main can unlock its cudaipcsrc.
func runChildPipeline(pipeline *gst.Pipeline, lockedSrc *gst.Element, sinkHasDataCh <-chan struct{}, probe *cProbe, cprobePath string, sendIPC func(string), fromMain <-chan string, dotDir, dotBase, elemName string) error {
	// PAUSED first; WAIT for completion so unixfdsrc has actually
	// connected to main's sockIn and unixfdsink is bound/listening
	// on main's sockOut (both happen in READY->PAUSED start()).
	if err := pipeline.SetState(gst.StatePaused); err != nil {
		dumpPipeline(pipeline, dotDir, dotBase+"_paused_fail")
		return fmt.Errorf("SetState PAUSED: %w", err)
	}
	cr, _ := pipeline.GetState(gst.StatePaused, gst.ClockTime(10*time.Second))
	if cr != gst.StateChangeSuccess && cr != gst.StateChangeNoPreroll {
		dumpPipeline(pipeline, dotDir, dotBase+"_paused_fail")
		_ = pipeline.SetState(gst.StateNull)
		return fmt.Errorf("pipeline failed to reach PAUSED: %s", cr.String())
	}
	dumpPipeline(pipeline, dotDir, dotBase+"_paused")

	sendIPC("ready")
	fmt.Fprintf(os.Stderr, "gstbench child[%s]: PAUSED reached, going to PLAYING\n", elemName)

	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		dumpPipeline(pipeline, dotDir, dotBase+"_playing_fail")
		_ = pipeline.SetState(gst.StateNull)
		return fmt.Errorf("SetState PLAYING: %w", err)
	}
	sendIPC("playing")

	// If the return IPC is cudaipc, start a goroutine that sends
	// "sink-has-data" to main once the first buffer reaches the
	// child's cudaipcsink server.
	if sinkHasDataCh != nil {
		go func() {
			<-sinkHasDataCh
			fmt.Fprintf(os.Stderr, "gstbench child[%s]: cudaipcsink has data, signalling main\n", elemName)
			sendIPC("sink-has-data")
		}()
	}

	// If the forward-path cudaipcsrc was locked, wait for main's
	// "sink-has-data" signal (proof that main's cudaipcsink server
	// has received its first buffer and CONFIG will be sent).
	if lockedSrc != nil {
		fmt.Fprintf(os.Stderr, "gstbench child[%s]: waiting for main sink-has-data\n", elemName)
		if err := waitFromMain(fromMain, "sink-has-data", 10*time.Second); err != nil {
			_ = pipeline.SetState(gst.StateNull)
			return fmt.Errorf("cudaipcsrc handshake: %w", err)
		}
		if err := lockedSrc.SetLockedState(false); err != nil {
			_ = pipeline.SetState(gst.StateNull)
			return fmt.Errorf("unlock cudaipcsrc: %w", err)
		}
		if ok := lockedSrc.SyncStateWithParent(); !ok {
			_ = pipeline.SetState(gst.StateNull)
			return fmt.Errorf("cudaipcsrc SyncStateWithParent returned false")
		}
		fmt.Fprintf(os.Stderr, "gstbench child[%s]: cudaipcsrc unlocked via sink-has-data handshake\n", elemName)
	}

	// Dump _playing AFTER all IPC elements are unlocked and
	// connected so the dot captures fully negotiated caps.
	dumpPipeline(pipeline, dotDir, dotBase+"_playing")

	fmt.Fprintf(os.Stderr, "gstbench child[%s]: PLAYING set, entering bus loop\n", elemName)

	// Listen for "forward-eos" from the runner. cudaipcsink (mmap)
	// does not forward EOS via IPC, so the runner detects it with a
	// pad probe and tells us explicitly. We then inject EOS into the
	// pipeline so it propagates through the EUT to the return-path
	// IPC sink.
	go func() {
		for msg := range fromMain {
			if msg == "forward-eos" {
				fmt.Fprintf(os.Stderr, "gstbench child[%s]: received forward-eos, sending EOS to pipeline\n", elemName)
				pipeline.SendEvent(gst.NewEOSEvent())
				return
			}
			fmt.Fprintf(os.Stderr, "gstbench child[%s]: unexpected from runner in bus loop: %q\n", elemName, msg)
		}
	}()

	bus := pipeline.GetPipelineBus()
	deadline := time.Now().Add(90 * time.Second)
	eos := false
	for time.Now().Before(deadline) && !eos {
		msg := bus.TimedPop(gst.ClockTime(time.Second))
		if msg == nil {
			continue
		}
		switch msg.Type() {
		case gst.MessageEOS:
			eos = true
		case gst.MessageError:
			gerr := msg.ParseError()
			dumpPipeline(pipeline, dotDir, dotBase+"_error")
			_ = pipeline.SetState(gst.StateNull)
			return fmt.Errorf("pipeline error: %v", gerr.Error())
		}
	}
	if !eos {
		fmt.Fprintf(os.Stderr, "gstbench child[%s]: 90s deadline hit, dumping\n", elemName)
		dumpPipeline(pipeline, dotDir, dotBase+"_timeout")
		// Hard-abort if SetState(NULL) hangs so main doesn't spin
		// until its own deadline.
		go func() {
			time.Sleep(5 * time.Second)
			fmt.Fprintf(os.Stderr, "gstbench child[%s]: SetState(NULL) hung >5s, os.Exit\n", elemName)
			os.Exit(2)
		}()
		_ = pipeline.SetState(gst.StateNull)
		return fmt.Errorf("child timed out waiting for EOS (90s child deadline)")
	}
	fmt.Fprintf(os.Stderr, "gstbench child[%s]: EOS, tearing down\n", elemName)

	if cprobePath != "" && probe != nil {
		if err := probe.writeTo(cprobePath); err != nil {
			fmt.Fprintf(os.Stderr, "gstbench child[%s]: cprobe write: %v\n", elemName, err)
		}
	}

	sendIPC("eos")

	if err := pipeline.SetState(gst.StateNull); err != nil {
		return fmt.Errorf("SetState NULL: %w", err)
	}
	return nil
}
