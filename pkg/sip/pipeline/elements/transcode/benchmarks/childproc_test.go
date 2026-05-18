package benchmarks

// Child process management from the main side: spawn, IPC handshake,
// kill/wait, and CPU accounting via /proc/<pid>/stat.
//
// The child is this same test binary re-exec'd via /proc/self/exe
// with GSTBENCH_ROLE=child. TestMain routes child-mode invocations to
// runChild before the testing framework starts, so there is no go
// build step and no version skew between parent and child.
//
// IPC uses two inherited pipes:
//   fd 3 (child→main): "ready" / "playing" / "sink-has-data" / "eos" / "done" / "error:…"
//   fd 4 (main→child): "sink-has-data"
//
// Both are newline-delimited text. See child_test.go for the sender side.
//
// Out of band: "error: <msg>" on any runChildImpl failure. On
// error/close the main runner aborts fast rather than waiting for its
// own bus deadline.

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// childHandle is the main-side view of the child process. Created by
// spawnChild, destroyed by kill/wait.
type childHandle struct {
	logf func(string, ...any) // diagnostic logging (runner's stderr)

	cmd       *exec.Cmd
	stderrBuf *bytes.Buffer
	msgCh     chan string // closed when the child's fd 3 pipe closes
	readyR    *os.File   // main-side read end of fd 3

	// toChildW is the main-side write end of fd 4 (main→child).
	// Used to send "sink-has-data" when main's cudaipcsink receives
	// its first buffer so the child can unlock its cudaipcsrc.
	toChildW   *os.File
	toChildBuf *bufio.Writer

	// tracePath is the tempfs file the child's GStreamer is configured
	// to write its tracer log to (via GST_DEBUG_FILE set at spawn).
	// Main parses it after waitExit — parsing never runs in the child
	// so its CPU counter stays clean.
	tracePath string

	// eutChildren is the list of EUT bin child element names, sent by
	// the child as a "children:..." IPC line right after it builds its
	// pipeline. Used to filter the trace-log parse so only the
	// element-under-test appears in Result.Children.
	eutChildren []string

	waited bool
}

// spawnChild builds the exec.Cmd, starts the child, and launches the
// IPC reader goroutine. The caller must at minimum call h.kill() or
// h.waitExit() eventually. tmp is a per-test scratch dir (typically
// t.TempDir()) where main will place the child's tracer log.
//
// srcMem/sinkMem are the memory types main's buildMainPipeline chose
// for the forward and return IPC channels. The child uses them to
// instantiate matching unixfd / cudaipc elements — same values on
// both sides of each unix socket, or the peers won't hand off.
func spawnChild(logf func(string, ...any), elem Element, cfg Config, sockIn, sockOut, tmp string, srcMem, sinkMem int) (*childHandle, error) {

	readyR, readyW, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("pipe (child→main): %w", err)
	}
	toChildR, toChildW, err := os.Pipe()
	if err != nil {
		_ = readyR.Close()
		_ = readyW.Close()
		return nil, fmt.Errorf("pipe (main→child): %w", err)
	}

	absDotDir, _ := filepath.Abs(DotDir)
	tracePath := filepath.Join(tmp, "child_trace.log")

	cmd := exec.Command("/proc/self/exe")
	cmd.Env = append(os.Environ(),
		"GSTBENCH_ROLE=child",
		"GSTBENCH_ELEMENT="+elem.Name(),
		fmt.Sprintf("GSTBENCH_TARGET_W=%d", cfg.TargetWidth),
		fmt.Sprintf("GSTBENCH_TARGET_H=%d", cfg.TargetHeight),
		"GSTBENCH_SOCK_IN="+sockIn,
		"GSTBENCH_SOCK_OUT="+sockOut,
		"GSTBENCH_DOT_DIR="+absDotDir,
		fmt.Sprintf("GSTBENCH_SOURCE_W=%d", cfg.SourceWidth),
		fmt.Sprintf("GSTBENCH_SOURCE_H=%d", cfg.SourceHeight),
		fmt.Sprintf("GSTBENCH_SRC_MEM=%d", srcMem),
		fmt.Sprintf("GSTBENCH_SINK_MEM=%d", sinkMem),
		// GStreamer trace config: latency tracer only (rusage is
		// redundant — we read /proc/<pid>/stat from main instead).
		// The child never reads GST_DEBUG_FILE back; main does,
		// after the child has exited, so this I/O is the only
		// measurement-time tracer cost on the child side.
		"GST_TRACERS=latency(flags=element)",
		"GST_DEBUG_FILE="+tracePath,
	)
	// GST_DEBUG: always include GST_TRACER:7 (needed so the latency
	// tracer actually writes its entries), and prepend anything the
	// parent shell already set so we can turn on extra categories
	// for debugging a specific failure without having to recompile.
	parentDebug := os.Getenv("GST_DEBUG")
	if parentDebug == "" {
		cmd.Env = append(cmd.Env, "GST_DEBUG=GST_TRACER:7")
	} else {
		cmd.Env = append(cmd.Env, "GST_DEBUG="+parentDebug+",GST_TRACER:7")
	}
	cmd.ExtraFiles = []*os.File{readyW, toChildR} // fd 3 = child→main, fd 4 = main→child
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf
	// Setsid puts the child in its own session so it survives parent
	// signal groups; Pdeathsig kills it if the parent dies so a
	// crashed test doesn't leave orphan children.
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid:    true,
		Pdeathsig: syscall.SIGKILL,
	}
	if err := cmd.Start(); err != nil {
		_ = readyR.Close()
		_ = readyW.Close()
		_ = toChildR.Close()
		_ = toChildW.Close()
		return nil, fmt.Errorf("spawn: %w", err)
	}
	_ = readyW.Close()  // parent doesn't need the write end
	_ = toChildR.Close() // parent doesn't need the read end

	msgCh := make(chan string, 16)
	go func() {
		defer close(msgCh)
		scanner := bufio.NewScanner(readyR)
		for scanner.Scan() {
			msgCh <- scanner.Text()
		}
	}()

	return &childHandle{
		logf:       logf,
		cmd:        cmd,
		stderrBuf:  &stderrBuf,
		msgCh:      msgCh,
		readyR:     readyR,
		toChildW:   toChildW,
		toChildBuf: bufio.NewWriter(toChildW),
		tracePath:  tracePath,
	}, nil
}

// absorb handles out-of-band IPC messages that can arrive at any
// phase. It recognises "children:a,b,c" and stashes the list; any
// other message is returned to the caller to deal with. Returns
// (true, "") if the message was absorbed.
func (h *childHandle) absorb(msg string) (absorbed bool) {
	if strings.HasPrefix(msg, "children:") {
		list := strings.TrimPrefix(msg, "children:")
		if list == "" {
			h.eutChildren = nil
		} else {
			h.eutChildren = strings.Split(list, ",")
		}
		return true
	}
	return false
}

// stderr returns whatever the child has written to stderr so far.
// Safe to call at any time.
func (h *childHandle) stderr() string {
	return h.stderrBuf.String()
}

// sendToChild sends a newline-delimited message to the child via fd 4.
func (h *childHandle) sendToChild(msg string) {
	if h.toChildBuf == nil {
		return
	}
	_, _ = h.toChildBuf.WriteString(msg + "\n")
	_ = h.toChildBuf.Flush()
}

// waitMsg blocks until the child sends the expected message or the
// hard timeout expires. A soft "WARN" fires at d/2 if the message has
// not arrived yet so the test log surfaces slow handshakes.
// "children:..." messages are absorbed silently into h.eutChildren.
func (h *childHandle) waitMsg(want string, d time.Duration) error {
	start := time.Now()
	slowAt := time.After(d / 2)
	deadline := time.After(d)
	warned := false
	for {
		select {
		case msg, ok := <-h.msgCh:
			if !ok {
				return fmt.Errorf("child pipe closed before %q (stderr: %s)", want, h.stderr())
			}
			if strings.HasPrefix(msg, "error:") {
				return fmt.Errorf("child reported %s", msg)
			}
			if h.absorb(msg) {
				continue
			}
			if msg == want {
				if warned {
					h.logf("WARN: child msg %q took %v (limit %v)", want, time.Since(start), d)
				}
				return nil
			}
			h.logf("child: unexpected %q while waiting for %q", msg, want)
		case <-slowAt:
			if !warned {
				h.logf("WARN: still waiting for child msg %q after %v (limit %v)", want, d/2, d)
				warned = true
			}
		case <-deadline:
			return fmt.Errorf("HARD TIMEOUT waiting for child %q after %v (stderr: %s)", want, d, h.stderr())
		}
	}
}

// drainStatus drains the msgCh non-blockingly, handling the subset of
// messages that may arrive during the bus loop: "error:", "eos",
// "done", or pipe-closed. Returns a non-nil error on out-of-band
// child error; otherwise updates eosSeen and returns nil. The bool is
// true iff the pipe is closed (no more messages coming).
func (h *childHandle) drainStatus(eosSeen *bool) (closed bool, err error) {
	for {
		select {
		case msg, ok := <-h.msgCh:
			if !ok {
				return true, nil
			}
			switch {
			case strings.HasPrefix(msg, "error:"):
				return false, fmt.Errorf("child reported %s", msg)
			case msg == "eos":
				*eosSeen = true
			case msg == "done":
				// Informational: child is exiting. Bus EOS usually
				// lands within a few ms.
			case msg == "sink-has-data":
				// Late arrival — the handshake already consumed this
				// via waitMsg. Log and move on.
				h.logf("child: late sink-has-data during bus loop")
			default:
				if h.absorb(msg) {
					continue
				}
				h.logf("child: %q", msg)
			}
		default:
			return false, nil
		}
	}
}

// kill forcefully terminates the child and drains its stderr via
// cmd.Wait. We use cmd.Wait (not os.Process.Wait) because exec.Cmd
// owns the stderr pipe goroutine; os.Process.Wait would reap the
// zombie but leave the stderr goroutine blocked in io.Copy, leaking
// into subsequent subtests.
func (h *childHandle) kill() {
	if h.waited {
		return
	}
	h.waited = true
	_ = h.readyR.Close()
	if h.toChildW != nil {
		_ = h.toChildW.Close()
	}
	if h.cmd.Process != nil {
		_ = h.cmd.Process.Kill()
		_ = h.cmd.Wait()
	}
}

// waitExit expects the child to exit cleanly on its own after main
// has seen EOS. Warns at 1s, hard-fails at 5s.
func (h *childHandle) waitExit() error {
	if h.waited {
		return nil
	}
	start := time.Now()
	waitCh := make(chan error, 1)
	go func() { waitCh <- h.cmd.Wait() }()
	slowCh := time.After(1 * time.Second)
	for {
		select {
		case werr := <-waitCh:
			h.waited = true
			_ = h.readyR.Close()
			if h.toChildW != nil {
				_ = h.toChildW.Close()
			}
			if time.Since(start) > 1*time.Second {
				h.logf("WARN: child cmd.Wait took %v (expected <1s)", time.Since(start))
			}
			if werr != nil {
				return fmt.Errorf("child exited nonzero: %w\nchild stderr: %s", werr, h.stderr())
			}
			return nil
		case <-slowCh:
			h.logf("WARN: child still not exited after 1s (hard limit 5s)")
			slowCh = nil
		case <-time.After(5 * time.Second):
			h.kill()
			return fmt.Errorf("HARD TIMEOUT: child did not exit within 5s after main EOS\nchild stderr: %s", h.stderr())
		}
	}
}

// cpuProbeInterval is the /proc sampling period for cpuProbe. The
// kernel updates utime/stime at clock-tick granularity (10 ms on
// mainstream Debian), so sampling faster than ~50 ms mostly yields
// duplicate reads. 100 ms gives ~50 samples over a 5 s run — thin
// for p99 but fine for p50/p95/mean, and each sample still contains
// ~10 ticks of real signal.
const cpuProbeInterval = 100 * time.Millisecond

// cpuSample is one raw snapshot of /proc/<pid>/stat.
type cpuSample struct {
	t     time.Time
	cpuNs uint64
}

// cpuLoadStats is the distribution of the child's whole-process CPU
// load over the active measurement window, in percent. Values can
// exceed 100% when the child uses more than one core. Computed on
// the steady-state middle 80% of samples (trimRatio on each end).
type cpuLoadStats struct {
	Samples       int
	Min, Max      float64
	Mean          float64
	P50, P90      float64
}

// cpuProbe periodically samples the child's whole-process CPU
// counter so main can report p50/p95/p99/mean load over the window
// where the child is actually doing work. Sampling starts when
// startCh is closed (the runner closes it the moment the first
// buffer comes back from the child — proof the pipeline is
// producing) and stops when the runner calls stop() after bus EOS.
//
// Both the start-signal and stop-signal run via channels so the
// probe can own its own goroutine lifetime without the runner
// having to poll.
type cpuProbe struct {
	pid      int
	interval time.Duration
	startCh  <-chan struct{}

	samples []cpuSample
	stopCh  chan struct{}
	doneCh  chan struct{}
}

// newCPUProbe creates a probe that will sample pid every interval
// once startCh is closed, until stop() is called.
func newCPUProbe(pid int, interval time.Duration, startCh <-chan struct{}) *cpuProbe {
	return &cpuProbe{
		pid:      pid,
		interval: interval,
		startCh:  startCh,
		stopCh:   make(chan struct{}),
		doneCh:   make(chan struct{}),
	}
}

// start launches the sampling goroutine. Safe to call once.
func (p *cpuProbe) start() {
	go p.run()
}

func (p *cpuProbe) run() {
	defer close(p.doneCh)

	// Block until the start signal fires or we are stopped before
	// ever starting (e.g. error path before first buffer arrived).
	select {
	case <-p.startCh:
	case <-p.stopCh:
		return
	}

	sample := func() {
		cpuNs, err := readSchedstatRuntime(p.pid)
		if err != nil {
			return
		}
		p.samples = append(p.samples, cpuSample{t: time.Now(), cpuNs: cpuNs})
	}
	// First sample captures t0; subsequent samples let loadStats
	// compute adjacent deltas.
	sample()
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		select {
		case <-p.stopCh:
			sample() // final sample
			return
		case <-ticker.C:
			sample()
		}
	}
}

// stop signals the sampling goroutine to exit and blocks until it
// has written its final sample. Safe to call multiple times.
func (p *cpuProbe) stop() {
	select {
	case <-p.stopCh:
		// already stopped
	default:
		close(p.stopCh)
	}
	<-p.doneCh
}

// loadStats computes the per-interval CPU-load distribution from the
// collected samples. Each delta is (cpu_ns[i]-cpu_ns[i-1]) /
// (wall_ns[i]-wall_ns[i-1]) * 100, i.e. the fraction of wall time
// the child was on CPU during that interval.
//
// Two trimming steps are applied:
//
//  1. If cutoff is non-zero, samples taken after cutoff are dropped
//     before computing deltas — honouring "stop measuring when the
//     child stops producing". The runner passes the residency
//     probe's last-exit timestamp here.
//
//  2. The resulting time-ordered load series is trimmed another
//     trimRatio from head and tail so only the steady-state middle
//     80% contributes to min/max/mean/p50/p90.
//
// Returns zeros if too few samples survive the trims.
func (p *cpuProbe) loadStats(cutoff time.Time) cpuLoadStats {
	samples := p.samples
	if !cutoff.IsZero() {
		n := len(samples)
		for i, s := range samples {
			if s.t.After(cutoff) {
				n = i
				break
			}
		}
		samples = samples[:n]
	}
	if len(samples) < 2 {
		return cpuLoadStats{}
	}
	// Time-ordered load deltas (raw).
	loads := make([]float64, 0, len(samples)-1)
	for i := 1; i < len(samples); i++ {
		cpuDelta := float64(samples[i].cpuNs - samples[i-1].cpuNs)
		wallDelta := float64(samples[i].t.Sub(samples[i-1].t).Nanoseconds())
		if wallDelta <= 0 {
			continue
		}
		loads = append(loads, cpuDelta/wallDelta*100.0)
	}
	// Chronological trim: drop first/last trimRatio of the load
	// series so startup ramp and any tail quiesce don't contribute.
	trim := int(float64(len(loads)) * trimRatio)
	if len(loads)-2*trim < 1 {
		return cpuLoadStats{}
	}
	loads = loads[trim : len(loads)-trim]
	// Sort to compute order statistics.
	sort.Float64s(loads)
	pct := func(q float64) float64 {
		idx := int(float64(len(loads)-1) * q)
		return loads[idx]
	}
	var sum float64
	for _, l := range loads {
		sum += l
	}
	return cpuLoadStats{
		Samples: len(loads),
		Min:     loads[0],
		Max:     loads[len(loads)-1],
		Mean:    sum / float64(len(loads)),
		P50:     pct(0.50),
		P90:     pct(0.90),
	}
}

// readSchedstatRuntime returns the total on-CPU time in ns for the
// given process (summed across all threads). Reads /proc/<pid>/stat
// and computes (utime+stime)*(1e9/CLK_TCK).
//
// We intentionally avoid /proc/<pid>/schedstat: its fields only
// update when sysctl kernel.sched_schedstats=1, which is off by
// default on modern Debian kernels, and the file succeeds silently
// with zeros when disabled — an attractive but dangerous first
// choice. /proc/<pid>/stat always works.
//
// Granularity is clock ticks (typically 10 ms). For a multi-second
// benchmark this is fine; for very short runs consider more buffers.
func readSchedstatRuntime(pid int) (uint64, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, err
	}
	// comm (field 2) can contain arbitrary bytes including spaces and
	// parens, so split on the last ')'.
	s := string(b)
	idx := strings.LastIndex(s, ")")
	if idx < 0 {
		return 0, fmt.Errorf("parse stat: missing comm end")
	}
	rest := strings.Fields(s[idx+1:])
	// rest[0] = state. utime/stime are fields 14/15 overall, which
	// is offset 11/12 in rest (post-comm tail).
	if len(rest) < 13 {
		return 0, fmt.Errorf("parse stat: too few fields after comm")
	}
	utime, err := strconv.ParseUint(rest[11], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse utime: %w", err)
	}
	stime, err := strconv.ParseUint(rest[12], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse stime: %w", err)
	}
	const clkTck = 100 // mainstream Linux default; unchanged on Debian
	return (utime + stime) * (1_000_000_000 / clkTck), nil
}

