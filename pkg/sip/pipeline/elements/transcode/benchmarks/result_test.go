package benchmarks

// Benchmark result types and table formatters. Kept separate from the
// runner so a reader can see the data shape without wading through
// pipeline / IPC plumbing.

import (
	"fmt"
	"strings"
	"time"

	"github.com/go-gst/go-gst/gst"
)

// Config describes one benchmark run.
type Config struct {
	SourceWidth  int `json:"source_width"`
	SourceHeight int `json:"source_height"`
	SourceFPS    int `json:"source_fps"`
	NumBuffers   int `json:"num_buffers"`
	TargetWidth  int `json:"target_width"`
	TargetHeight int `json:"target_height"`
}

// MemSystem and MemCUDA are the memory-type values returned by
// BuildSource/BuildSink. Hardcoded ints so per-element zz_testutils.go
// files (production code in sibling packages) don't need to import
// anything from this test-only package.
//
//	0 = MemSystem — system / gst / fd-backed memory; carried by unixfd IPC
//	1 = MemCUDA   — video/x-raw(memory:CUDAMemory);  carried by cudaipc IPC
//
// Order matters — do not reorder. If a new memory type (e.g. VAAPI,
// DMABUF) is added, append it with a new integer and update
// ipcSinkFor/ipcSrcFor + per-element files.
const (
	MemSystem = 0
	MemCUDA   = 1
)

// Element is the structural contract the benchmarks runner expects
// from each transcode element.
//
// BuildSource builds the upstream chain and returns (pad, memType,
// err). The memType indicates what the returned pad emits so the
// runner can pick the correct IPC sink: MemSystem -> unixfdsink,
// MemCUDA -> cudaipcsink.
//
// BuildSink builds a sink chain and returns (pad, memType, err).
// The pad is the chain's INPUT, and memType is the memory type that
// pad accepts — which equals the memory type the EUT emits on its
// src pad. The runner picks the matching IPC source element the
// same way.
//
// BuildSink chains must set both sync=false and async=false on their
// terminal fakesink so the pipeline transitions cleanly and data is
// consumed as fast as the producer provides.
type Element interface {
	Name() string
	BuildSource(p *gst.Pipeline, width, height, fps, numBuffers int) (src *gst.Pad, memType int, err error)
	BuildElement(p *gst.Pipeline, targetWidth, targetHeight int) (*gst.Element, error)
	BuildSink(p *gst.Pipeline) (sink *gst.Pad, memType int, err error)
}

// ChildStats is the latency-tracer percentile summary for one
// internal child of the EUT bin. Populated by main after the child
// process has exited by parsing its GST_DEBUG_FILE trace log.
// Computed on the steady-state middle 80% (trimRatio trim from each
// end), so Min/Max are the steady-state min/max, not the overall.
type ChildStats struct {
	Name  string        `json:"name"`
	Count int           `json:"count"`
	Min   time.Duration `json:"min"`
	Max   time.Duration `json:"max"`
	Mean  time.Duration `json:"mean"`
	P50   time.Duration `json:"p50"`
	P90   time.Duration `json:"p90"`
}

// Result is everything one benchmark run produced.
//
//   - Residency — pure-C pad probes on the EUT's ghost pads in the
//     child process (see cprobe.c). Measures the element-under-test
//     latency directly, without IPC transport overhead.
//   - CPU load  — /proc/<child-pid>/stat around the wall window
//     (utime+stime, so it covers every worker thread the EUT
//     spawns, whether or not GStreamer sees them).
//   - Children  — per-element percentiles from the GST latency
//     tracer, parsed from the child's trace log after it exits.
type Result struct {
	Config      Config `json:"config"`
	ElementName string `json:"element_name"`

	ResidencySamples int           `json:"residency_samples"`
	ResidencyMin     time.Duration `json:"residency_min"`
	ResidencyMax     time.Duration `json:"residency_max"`
	ResidencyMean    time.Duration `json:"residency_mean"`
	ResidencyP50     time.Duration `json:"residency_p50"`
	ResidencyP90     time.Duration `json:"residency_p90"`

	Children []ChildStats  `json:"children,omitempty"`
	SumMean  time.Duration `json:"sum_mean"`

	CrossRatio float64 `json:"cross_ratio"`

	CPULoadSamples int     `json:"cpu_load_samples"`
	CPULoadMin     float64 `json:"cpu_load_min"`
	CPULoadMax     float64 `json:"cpu_load_max"`
	CPULoadMean    float64 `json:"cpu_load_mean"`
	CPULoadP50     float64 `json:"cpu_load_p50"`
	CPULoadP90     float64 `json:"cpu_load_p90"`
}

// dominantChild returns the child with the highest mean latency, or
// a zero-value ChildStats if there are none.
func dominantChild(r Result) ChildStats {
	var dom ChildStats
	for _, c := range r.Children {
		if c.Mean > dom.Mean {
			dom = c
		}
	}
	return dom
}

// FormatMarkdown returns a markdown document with one `# element`
// section per unique ElementName, in the order the results first
// appear. Residency is main-side (includes IPC tax); cpu_load is
// whole-process child /proc counters; dominant_child is the slowest
// EUT child element by tracer-measured mean latency, useful for
// spotting which internal element is the bottleneck.
func FormatMarkdown(results []Result) string {
	var b strings.Builder
	prev := ""
	for _, r := range results {
		if r.ElementName != prev {
			if prev != "" {
				fmt.Fprintln(&b)
			}
			fmt.Fprintf(&b, "# %s\n\n", r.ElementName)
			fmt.Fprintln(&b, "| source | target | res_mean | res_p50 | res_p90 | res_max | sum_mean | cross | cpu_mean | cpu_p50 | cpu_p90 | cpu_max | dominant_child |")
			fmt.Fprintln(&b, "|---|---|---|---|---|---|---|---|---|---|---|---|---|")
			prev = r.ElementName
		}
		src := fmt.Sprintf("%dx%d", r.Config.SourceWidth, r.Config.SourceHeight)
		dst := fmt.Sprintf("%dx%d", r.Config.TargetWidth, r.Config.TargetHeight)
		dom := dominantChild(r)
		domStr := "-"
		if dom.Name != "" {
			domStr = fmt.Sprintf("%s %v", dom.Name, dom.Mean)
		}
		fmt.Fprintf(&b, "| %s | %s | %v | %v | %v | %v | %v | %.2f | %.1f%% | %.1f%% | %.1f%% | %.1f%% | %s |\n",
			src, dst,
			r.ResidencyMean, r.ResidencyP50, r.ResidencyP90, r.ResidencyMax,
			r.SumMean, r.CrossRatio,
			r.CPULoadMean, r.CPULoadP50, r.CPULoadP90, r.CPULoadMax,
			domStr)
	}
	return b.String()
}

// FormatCSV returns the results as CSV with a single header row
// covering all elements. Durations are emitted in nanoseconds so
// numbers round-trip cleanly into spreadsheets and pandas.
func FormatCSV(results []Result) string {
	var b strings.Builder
	fmt.Fprintln(&b, "element,source_w,source_h,target_w,target_h,fps,num_buffers,"+
		"res_min_ns,res_max_ns,res_mean_ns,res_p50_ns,res_p90_ns,res_samples,"+
		"sum_mean_ns,cross_ratio,"+
		"cpu_min,cpu_max,cpu_mean,cpu_p50,cpu_p90,cpu_samples,"+
		"dominant_child,dominant_child_mean_ns")
	for _, r := range results {
		dom := dominantChild(r)
		fmt.Fprintf(&b, "%s,%d,%d,%d,%d,%d,%d,"+
			"%d,%d,%d,%d,%d,%d,"+
			"%d,%.4f,"+
			"%.3f,%.3f,%.3f,%.3f,%.3f,%d,"+
			"%s,%d\n",
			r.ElementName,
			r.Config.SourceWidth, r.Config.SourceHeight,
			r.Config.TargetWidth, r.Config.TargetHeight,
			r.Config.SourceFPS, r.Config.NumBuffers,
			r.ResidencyMin.Nanoseconds(), r.ResidencyMax.Nanoseconds(),
			r.ResidencyMean.Nanoseconds(),
			r.ResidencyP50.Nanoseconds(), r.ResidencyP90.Nanoseconds(),
			r.ResidencySamples,
			r.SumMean.Nanoseconds(), r.CrossRatio,
			r.CPULoadMin, r.CPULoadMax, r.CPULoadMean, r.CPULoadP50, r.CPULoadP90,
			r.CPULoadSamples,
			dom.Name, dom.Mean.Nanoseconds(),
		)
	}
	return b.String()
}
