package benchmarks

// Runner process entry point. Re-exec'd by the orchestrator (Run)
// with GSTBENCH_ROLE=runner. Builds the main pipeline, spawns the
// child, runs the measurement, writes Result JSON, exits.
//
// This runs in a separate process so neither it nor the child
// inherits CUDA driver state from the orchestrator (which never
// calls gst.Init). See experiments/exp_3proc for verification.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/go-gst/go-gst/gst"
)

// runnerEnv collects the inputs runRunner needs from GSTBENCH_* env vars.
type runnerEnv struct {
	elemName   string
	sourceW    int
	sourceH    int
	sourceFPS  int
	numBuffers int
	targetW    int
	targetH    int
	sockIn     string
	sockOut    string
	dotDir     string
	tmpDir     string
	resultPath string
	tracePath  string
	cprobePath string
	childPID   int
}

func parseRunnerEnv() (*runnerEnv, error) {
	e := &runnerEnv{
		elemName:   os.Getenv("GSTBENCH_ELEMENT"),
		sockIn:     os.Getenv("GSTBENCH_SOCK_IN"),
		sockOut:    os.Getenv("GSTBENCH_SOCK_OUT"),
		dotDir:     os.Getenv("GSTBENCH_DOT_DIR"),
		tmpDir:     os.Getenv("GSTBENCH_TMP_DIR"),
		resultPath: os.Getenv("GSTBENCH_RESULT_PATH"),
	}
	e.tracePath = os.Getenv("GSTBENCH_TRACE_PATH")
	e.cprobePath = os.Getenv("GSTBENCH_CPROBE_PATH")
	if e.elemName == "" || e.sockIn == "" || e.sockOut == "" || e.resultPath == "" {
		return nil, fmt.Errorf("missing required env (ELEMENT/SOCK_IN/SOCK_OUT/RESULT_PATH)")
	}
	var err error
	if e.childPID, err = strconv.Atoi(os.Getenv("GSTBENCH_CHILD_PID")); err != nil {
		return nil, fmt.Errorf("CHILD_PID: %w", err)
	}
	if e.sourceW, err = strconv.Atoi(os.Getenv("GSTBENCH_SOURCE_W")); err != nil {
		return nil, fmt.Errorf("SOURCE_W: %w", err)
	}
	if e.sourceH, err = strconv.Atoi(os.Getenv("GSTBENCH_SOURCE_H")); err != nil {
		return nil, fmt.Errorf("SOURCE_H: %w", err)
	}
	if e.sourceFPS, err = strconv.Atoi(os.Getenv("GSTBENCH_SOURCE_FPS")); err != nil {
		return nil, fmt.Errorf("SOURCE_FPS: %w", err)
	}
	if e.numBuffers, err = strconv.Atoi(os.Getenv("GSTBENCH_NUM_BUFFERS")); err != nil {
		return nil, fmt.Errorf("NUM_BUFFERS: %w", err)
	}
	if e.targetW, err = strconv.Atoi(os.Getenv("GSTBENCH_TARGET_W")); err != nil {
		return nil, fmt.Errorf("TARGET_W: %w", err)
	}
	if e.targetH, err = strconv.Atoi(os.Getenv("GSTBENCH_TARGET_H")); err != nil {
		return nil, fmt.Errorf("TARGET_H: %w", err)
	}
	return e, nil
}

func runnerLogf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "gstbench runner: "+format+"\n", args...)
}

func runRunner() {
	fmt.Fprintf(os.Stderr, "gstbench runner: started (pid %d)\n", os.Getpid())
	env, err := parseRunnerEnv()
	if err != nil {
		fmt.Fprintf(os.Stderr, "gstbench runner: %v\n", err)
		os.Exit(1)
	}
	elem, err := lookupElement(env.elemName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gstbench runner: %v\n", err)
		os.Exit(1)
	}
	cfg := Config{
		SourceWidth:  env.sourceW,
		SourceHeight: env.sourceH,
		SourceFPS:    env.sourceFPS,
		NumBuffers:   env.numBuffers,
		TargetWidth:  env.targetW,
		TargetHeight: env.targetH,
	}
	result, err := runRunnerImpl(elem, cfg, env)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gstbench runner: %v\n", err)
		os.Exit(1)
	}
	data, err := json.Marshal(result)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gstbench runner: marshal: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(env.resultPath, data, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "gstbench runner: write result: %v\n", err)
		os.Exit(1)
	}
	os.Exit(0)
}

// runRunnerImpl builds the main pipeline, communicates with the child
// (already spawned by the orchestrator), runs the handshake + bus loop,
// composes the result, and returns it.
//
// The runner inherits IPC pipes from the orchestrator:
//   fd 3 = read end of child→runner pipe
//   fd 4 = write end of runner→child pipe
func runRunnerImpl(elem Element, cfg Config, env *runnerEnv) (Result, error) {
	dotBase := fmt.Sprintf("%s_%dx%d_to_%dx%d",
		elem.Name(), cfg.SourceWidth, cfg.SourceHeight, cfg.TargetWidth, cfg.TargetHeight)

	// ---- 1. Build main pipeline ----
	mp, err := buildMainPipeline(elem, cfg, env.sockIn, env.sockOut)
	if err != nil {
		return Result{}, fmt.Errorf("buildMainPipeline: %w", err)
	}

	// ---- 2. Main -> PAUSED ----
	if err := mp.pipeline.SetState(gst.StatePaused); err != nil {
		dumpPipeline(mp.pipeline, env.dotDir, dotBase+"_paused_fail")
		return Result{}, fmt.Errorf("SetState PAUSED: %w", err)
	}
	if cr, _ := mp.pipeline.GetState(gst.StatePaused, gst.ClockTime(10*time.Second)); cr != gst.StateChangeSuccess && cr != gst.StateChangeNoPreroll {
		dumpPipeline(mp.pipeline, env.dotDir, dotBase+"_paused_fail")
		_ = mp.pipeline.SetState(gst.StateNull)
		return Result{}, fmt.Errorf("pipeline did not reach PAUSED: %s", cr.String())
	}
	dumpPipeline(mp.pipeline, env.dotDir, dotBase+"_paused_locked")

	// ---- 3. Set up child handle from inherited fds ----
	child := newChildHandleFromFDs(runnerLogf, env.childPID, env.tracePath)

	// Send memory types to child so it can pick matching IPC elements.
	// The child waits for this before building its pipeline.
	child.sendToChild(fmt.Sprintf("mem:%d,%d", mp.srcMem, mp.sinkMem))

	bailOut := func(format string, args ...any) error {
		child.closeIPC()
		dumpPipeline(mp.pipeline, env.dotDir, dotBase+"_failed")
		_ = mp.pipeline.SetState(gst.StateNull)
		return fmt.Errorf(format, args...)
	}

	// ---- 4. Wait for child "ready" ----
	if err := child.waitMsg("ready", 10*time.Second); err != nil {
		return Result{}, bailOut("%v", err)
	}

	// ---- 5. Unlock main-side unixfdsrc clients ----
	if err := mp.unlockReadyClients(); err != nil {
		dumpPipeline(mp.pipeline, env.dotDir, dotBase+"_sync_fail")
		return Result{}, bailOut("%v", err)
	}

	// ---- 6. Wait for child "playing" ----
	if err := child.waitMsg("playing", 10*time.Second); err != nil {
		return Result{}, bailOut("%v", err)
	}

	// ---- 7. Start CPU + GPU samplers, main -> PLAYING ----
	cpuP := newCPUProbe(env.childPID, cpuProbeInterval, mp.firstExitCh)
	cpuP.start()
	defer cpuP.stop()

	if err := mp.pipeline.SetState(gst.StatePlaying); err != nil {
		dumpPipeline(mp.pipeline, env.dotDir, dotBase+"_playing_fail")
		return Result{}, bailOut("SetState PLAYING: %v", err)
	}

	// ---- 7a. CUDA forward handshake ----
	if mp.sinkHasDataCh != nil {
		go func() {
			select {
			case <-mp.sinkHasDataCh:
				child.sendToChild("sink-has-data")
			case <-time.After(10 * time.Second):
				fmt.Fprintf(os.Stderr, "WARN: main cudaipcsink probe did not fire within 10s\n")
			}
		}()
	}

	// ---- 7a'. Forward-path EOS relay ----
	// cudaipcsink (mmap) does NOT forward EOS via IPC. When the
	// source finishes and EOS reaches the forward-path sink pad,
	// send "forward-eos" to the child so it can inject EOS into
	// its pipeline and propagate it to the return path.
	go func() {
		select {
		case <-mp.fwdEOSCh:
			runnerLogf("forward-path EOS detected, signalling child")
			child.sendToChild("forward-eos")
		case <-time.After(195 * time.Second):
			// Safety net — if EOS never arrives, the bus loop
			// timeout will catch it.
		}
	}()

	// ---- 7b. CUDA return handshake ----
	if mp.cudaReturnSrc != nil {
		if err := child.waitMsg("sink-has-data", 10*time.Second); err != nil {
			return Result{}, bailOut("cudaipcsrc return handshake: %v", err)
		}
		if err := mp.unlockCudaReturnSrc(); err != nil {
			return Result{}, bailOut("unlockCudaReturnSrc: %v", err)
		}
	}

	// Wait for first exit buffer before dumping _playing dot.
	select {
	case <-mp.firstExitCh:
	case <-time.After(2 * time.Second):
		runnerLogf("WARN: no exit buffer within 2s")
	}
	dumpPipeline(mp.pipeline, env.dotDir, dotBase+"_playing")

	// ---- 8. Bus loop ----
	if err := runBusLoopRunner(mp.pipeline, child); err != nil {
		return Result{}, bailOut("%v", err)
	}
	eosTime := time.Now()
	cpuP.stop()

	// ---- 9. Teardown ----
	_ = mp.pipeline.SetState(gst.StateNull)
	child.closeIPC()
	// Wait a moment for the child to flush its trace log before we parse it.
	time.Sleep(100 * time.Millisecond)

	// ---- 10. Compose result ----
	result, err := composeResult(elem, cfg, env.cprobePath, env.tracePath, child.eutChildren, cpuP.loadStats(eosTime))
	if err != nil {
		return Result{}, fmt.Errorf("composeResult: %v", err)
	}
	return result, nil
}

// runBusLoopRunner is the runner-process version of runBusLoop.
// Same logic but uses runnerLogf instead of t.Logf.
func runBusLoopRunner(pipeline *gst.Pipeline, child *childHandle) error {
	const hardTimeout = 180 * time.Second
	bus := pipeline.GetPipelineBus()
	busStart := time.Now()
	deadline := busStart.Add(hardTimeout)
	warnAt := []time.Duration{15 * time.Second, 45 * time.Second, 90 * time.Second, 150 * time.Second}
	warnIdx := 0

	eos := false
	childEOSSeen := false
	var busErr error

	for time.Now().Before(deadline) && !eos && busErr == nil {
		if warnIdx < len(warnAt) && time.Since(busStart) >= warnAt[warnIdx] {
			runnerLogf("WARN: bus EOS wait at %v (hard limit %v, childEOSSeen=%v)", warnAt[warnIdx], hardTimeout, childEOSSeen)
			warnIdx++
		}

		closed, drainErr := child.drainStatus(&childEOSSeen)
		if drainErr != nil {
			busErr = drainErr
			break
		}
		if closed && !childEOSSeen {
			busErr = fmt.Errorf("child pipe closed before eos (stderr: %s)", child.stderr())
			break
		}

		msg := bus.TimedPop(gst.ClockTime(100 * time.Millisecond))
		if msg == nil {
			continue
		}
		switch msg.Type() {
		case gst.MessageEOS:
			eos = true
		case gst.MessageError:
			gerr := msg.ParseError()
			busErr = fmt.Errorf("pipeline error: %v", gerr.Error())
		}
	}

	if busErr == nil && !eos {
		busErr = fmt.Errorf("pipeline timed out waiting for EOS after %v", hardTimeout)
	}
	return busErr
}

// newChildHandleFromFDs creates a childHandle that uses inherited fds
// from the orchestrator. fd 3 = read from child, fd 4 = write to child.
// The runner does NOT own the child process — the orchestrator does.
func newChildHandleFromFDs(logf func(string, ...any), childPID int, tracePath string) *childHandle {
	readFromChild := os.NewFile(3, "child-ipc-read")
	writeToChild := os.NewFile(4, "child-ipc-write")

	msgCh := make(chan string, 16)
	go func() {
		defer close(msgCh)
		scanner := bufio.NewScanner(readFromChild)
		for scanner.Scan() {
			msgCh <- scanner.Text()
		}
	}()

	return &childHandle{
		logf:       logf,
		stderrBuf:  &bytes.Buffer{}, // empty — orchestrator captures child stderr
		msgCh:      msgCh,
		readyR:     readFromChild,
		toChildW:   writeToChild,
		toChildBuf: bufio.NewWriter(writeToChild),
		tracePath:  tracePath,
	}
}

// closeIPC closes the runner's IPC pipe ends. Called during teardown
// or on error paths.
func (h *childHandle) closeIPC() {
	if h.readyR != nil {
		h.readyR.Close()
		h.readyR = nil
	}
	if h.toChildW != nil {
		h.toChildW.Close()
		h.toChildW = nil
	}
}

// lookupElement is also used by the child; defined here for shared access.
func lookupElement(name string) (Element, error) {
	for _, e := range elementsUnderTest {
		if e.Name() == name {
			return e, nil
		}
	}
	return nil, fmt.Errorf("element %q not found in elementsUnderTest", name)
}

