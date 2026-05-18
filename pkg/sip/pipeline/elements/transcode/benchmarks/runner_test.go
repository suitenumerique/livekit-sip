// Package benchmarks runs comparative latency + CPU measurements
// across multiple transcode elements at a matrix of source/target
// resolutions.
//
// 3-process architecture:
//
//   Orchestrator (test process) — NO GStreamer, NO CUDA. Spawns:
//     Runner  (GSTBENCH_ROLE=runner) — main pipeline, probes, CPU sampler
//     Child   (GSTBENCH_ROLE=child)  — EUT pipeline (spawned by runner)
//
// The orchestrator never calls gst.Init so neither child inherits
// CUDA driver state. This is required for cudaipc mmap mode — see
// experiments/exp_3proc.
//
// IPC between runner and child uses two newline-delimited pipes:
//   fd 3 (child→runner): "ready" / "playing" / "sink-has-data" / "eos" / "done" / "error:…"
//   fd 4 (runner→child): "sink-has-data"
//
// File map:
//
//	runner_test.go      — Run() orchestrator + shared helpers (this file)
//	runnerfork_test.go  — runner process entry point (runRunner)
//	result_test.go      — Config / Element / Result / formatters
//	tracer_test.go      — GST_DEBUG trace-log parsing
//	mainpipe_test.go    — runner's GstPipeline builder + residency probes
//	childproc_test.go   — child spawn, IPC, /proc CPU accounting
//	child_test.go       — child-side pipeline + runChild entry point
//	zz_test.go          — TestMain + element registry + TestAllElements
package benchmarks

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/go-gst/go-gst/gst"
)

// DotDir is where per-cell pipeline dot graphs are written.
const DotDir = "testdata/dot"

func dumpPipeline(pipe *gst.Pipeline, dir, basename string) {
	if pipe == nil || dir == "" {
		return
	}
	data := pipe.DebugBinToDotData(gst.DebugGraphShowAll)
	_ = os.WriteFile(filepath.Join(dir, basename+".dot"), []byte(data), 0644)
}

func Run(t *testing.T, elem Element, cfg Config) Result {
	t.Helper()

	tmp := t.TempDir()
	sockIn := filepath.Join(tmp, "i.sock")
	sockOut := filepath.Join(tmp, "o.sock")
	if len(sockIn) > 107 || len(sockOut) > 107 {
		t.Fatalf("unix socket paths too long: in=%d out=%d (max 107)", len(sockIn), len(sockOut))
	}

	resultPath := filepath.Join(tmp, "result.json")
	absDotDir, _ := filepath.Abs(DotDir)
	tracePath := filepath.Join(tmp, "child_trace.log")
	cprobePath := filepath.Join(tmp, "child_cprobe.bin")

	// Create IPC pipes: child→runner (fd 3 in child, fd 3 in runner)
	// and runner→child (fd 4 in child, fd 4 in runner).
	c2rR, c2rW, err := os.Pipe() // child writes (fd 3), runner reads (fd 3)
	if err != nil {
		t.Fatalf("pipe c2r: %v", err)
	}
	r2cR, r2cW, err := os.Pipe() // runner writes (fd 4), child reads (fd 4)
	if err != nil {
		t.Fatalf("pipe r2c: %v", err)
	}

	// Common env shared by both processes.
	commonEnv := append(os.Environ(),
		"GSTBENCH_ELEMENT="+elem.Name(),
		fmt.Sprintf("GSTBENCH_SOURCE_W=%d", cfg.SourceWidth),
		fmt.Sprintf("GSTBENCH_SOURCE_H=%d", cfg.SourceHeight),
		fmt.Sprintf("GSTBENCH_TARGET_W=%d", cfg.TargetWidth),
		fmt.Sprintf("GSTBENCH_TARGET_H=%d", cfg.TargetHeight),
		"GSTBENCH_SOCK_IN="+sockIn,
		"GSTBENCH_SOCK_OUT="+sockOut,
		"GSTBENCH_DOT_DIR="+absDotDir,
	)

	// ---- Spawn child ----
	childCmd := exec.Command("/proc/self/exe")
	childCmd.Env = append(append([]string{}, commonEnv...),
		"GSTBENCH_ROLE=child",
		fmt.Sprintf("GSTBENCH_NUM_BUFFERS=%d", cfg.NumBuffers),
		"GSTBENCH_CPROBE_PATH="+cprobePath,
		"GST_TRACERS=latency(flags=element)",
		"GST_DEBUG_FILE="+tracePath,
	)
	// Merge GST_DEBUG from parent.
	parentDebug := os.Getenv("GST_DEBUG")
	if parentDebug == "" {
		childCmd.Env = append(childCmd.Env, "GST_DEBUG=GST_TRACER:7")
	} else {
		childCmd.Env = append(childCmd.Env, "GST_DEBUG="+parentDebug+",GST_TRACER:7")
	}
	// Child gets: fd 3 = c2rW (write to runner), fd 4 = r2cR (read from runner)
	childCmd.ExtraFiles = []*os.File{c2rW, r2cR}
	var childStderr bytes.Buffer
	childCmd.Stderr = io.MultiWriter(&childStderr, os.Stderr)
	childCmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid:    true,
		Pdeathsig: syscall.SIGKILL,
	}

	// ---- Spawn runner ----
	runnerCmd := exec.Command("/proc/self/exe")
	runnerCmd.Dir, _ = os.Getwd()
	runnerCmd.Env = append(append([]string{}, commonEnv...),
		"GSTBENCH_ROLE=runner",
		fmt.Sprintf("GSTBENCH_SOURCE_FPS=%d", cfg.SourceFPS),
		fmt.Sprintf("GSTBENCH_NUM_BUFFERS=%d", cfg.NumBuffers),
		"GSTBENCH_TMP_DIR="+tmp,
		"GSTBENCH_RESULT_PATH="+resultPath,
		"GSTBENCH_TRACE_PATH="+tracePath,
		"GSTBENCH_CPROBE_PATH="+cprobePath,
	)
	// Runner gets: fd 3 = c2rR (read from child), fd 4 = r2cW (write to child)
	runnerCmd.ExtraFiles = []*os.File{c2rR, r2cW}
	var runnerStderr bytes.Buffer
	runnerCmd.Stderr = io.MultiWriter(&runnerStderr, os.Stderr)
	runnerCmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid:    true,
		Pdeathsig: syscall.SIGKILL,
	}

	if err := childCmd.Start(); err != nil {
		t.Fatalf("spawn child: %v", err)
	}
	c2rW.Close()
	r2cR.Close()

	runnerCmd.Env = append(runnerCmd.Env,
		fmt.Sprintf("GSTBENCH_CHILD_PID=%d", childCmd.Process.Pid),
	)

	if err := runnerCmd.Start(); err != nil {
		childCmd.Process.Kill()
		childCmd.Wait()
		t.Fatalf("spawn runner: %v", err)
	}
	c2rR.Close()
	r2cW.Close()

	runnerDone := make(chan error, 1)
	go func() { runnerDone <- runnerCmd.Wait() }()

	childDone := make(chan error, 1)
	go func() { childDone <- childCmd.Wait() }()

	select {
	case err := <-runnerDone:
		if err != nil {
			childCmd.Process.Kill()
			childCmd.Wait()
			t.Fatalf("runner failed: %v\nrunner stderr:\n%s\nchild stderr:\n%s",
				err, runnerStderr.String(), childStderr.String())
		}
	case <-time.After(195 * time.Second):
		runnerCmd.Process.Kill()
		runnerCmd.Wait()
		childCmd.Process.Kill()
		childCmd.Wait()
		t.Fatalf("runner timeout\nrunner stderr:\n%s\nchild stderr:\n%s",
			runnerStderr.String(), childStderr.String())
	}

	select {
	case <-childDone:
	case <-time.After(5 * time.Second):
		childCmd.Process.Kill()
		childCmd.Wait()
	}

	data, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatalf("read result: %v\nrunner stderr:\n%s\nchild stderr:\n%s",
			err, runnerStderr.String(), childStderr.String())
	}
	var result Result
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	return result
}

// composeResult assembles a Result from the C probe latency file
// (written by the child), the CPU sampler stats, and the child's
// latency-tracer log.
func composeResult(elem Element, cfg Config, cprobePath, tracePath string, eutChildren []string, cpuStats cpuLoadStats) (Result, error) {
	resCopy, err := readCProbeFile(cprobePath)
	if err != nil {
		return Result{}, fmt.Errorf("%s %dx%d->%dx%d: read cprobe: %w",
			elem.Name(), cfg.SourceWidth, cfg.SourceHeight, cfg.TargetWidth, cfg.TargetHeight, err)
	}
	if len(resCopy) == 0 {
		return Result{}, fmt.Errorf("%s %dx%d->%dx%d: no latency samples from C probe",
			elem.Name(), cfg.SourceWidth, cfg.SourceHeight, cfg.TargetWidth, cfg.TargetHeight)
	}
	minSamples := int(float64(cfg.NumBuffers) * minResidencyRatio)
	if len(resCopy) < minSamples {
		return Result{}, fmt.Errorf("%s %dx%d->%dx%d: only %d C probe samples for %d buffers (< %.0f%%)",
			elem.Name(), cfg.SourceWidth, cfg.SourceHeight, cfg.TargetWidth, cfg.TargetHeight,
			len(resCopy), cfg.NumBuffers, minResidencyRatio*100)
	}

	resSorted, resMin, resMax, resMean, resP50, resP90 := percentileAndMean(resCopy)
	result := Result{
		Config:           cfg,
		ElementName:      elem.Name(),
		ResidencySamples: len(resSorted),
		ResidencyMin:     resMin,
		ResidencyMax:     resMax,
		ResidencyMean:    resMean,
		ResidencyP50:     resP50,
		ResidencyP90:     resP90,
	}

	if tracePath != "" && len(eutChildren) > 0 {
		filter := make(map[string]bool, len(eutChildren))
		for _, n := range eutChildren {
			filter[n] = true
		}
		samples, perr := parseTracerLog(tracePath, filter)
		if perr == nil {
			var sumMean time.Duration
			for _, n := range eutChildren {
				raw := samples[n]
				sorted, min, max, mean, p50, p90 := percentileAndMean(raw)
				result.Children = append(result.Children, ChildStats{
					Name:  n,
					Count: len(sorted),
					Min:   min,
					Max:   max,
					Mean:  mean,
					P50:   p50,
					P90:   p90,
				})
				sumMean += mean
			}
			result.SumMean = sumMean
			if sumMean > 0 {
				result.CrossRatio = float64(result.ResidencyMean) / float64(sumMean)
			}
		}
	}

	result.CPULoadSamples = cpuStats.Samples
	result.CPULoadMin = cpuStats.Min
	result.CPULoadMax = cpuStats.Max
	result.CPULoadMean = cpuStats.Mean
	result.CPULoadP50 = cpuStats.P50
	result.CPULoadP90 = cpuStats.P90

	return result, nil
}
