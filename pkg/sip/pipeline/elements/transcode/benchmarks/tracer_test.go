package benchmarks

// GST latency-tracer log parsing (main side only) and percentile
// math. The child runs with:
//
//	GST_TRACERS=latency(flags=element)
//	GST_DEBUG=GST_TRACER:7
//	GST_DEBUG_FILE=<tempfs path>
//
// which causes GStreamer to log per-buffer "element-latency" entries
// for every pad in the EUT bin. The child never reads this file — it
// is a tempfs write-only log. After the child exits cleanly, main
// reads the file and turns it into per-element percentile summaries
// (see parseTracerLog below).
//
// Parsing happens in main, after cmd.Wait, so trace overhead in the
// child is limited to the tracer probes themselves + writing
// formatted lines to tempfs. The CPU cost of parsing + sorting
// percentiles never lands on the child's /proc counter.

import (
	"bufio"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// trimRatio is the fraction of samples discarded from the HEAD
	// and from the TAIL of every time-ordered series before
	// computing min/max/mean/p50/p90. A 10% trim on both ends drops
	// codec warm-up, caps negotiation, and first-frame artefacts at
	// the start, plus any wind-down once data stops flowing at the
	// end. Only the steady-state middle 80% is analysed — the only
	// region where the element is actually running.
	trimRatio = 0.10
	// minResidencyRatio is the minimum fraction of numBuffers we
	// expect to see reach the sink via the residency probes. Anything
	// lower suggests the pipeline silently dropped frames and the
	// percentiles below would be computed on a biased sub-sample.
	minResidencyRatio = 0.70
)

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*[mGKH]`)

// element-latency, element-id=(string)0x..., element=(string)vp9dec0, src=(string)src, time=(guint64)1234567, ts=(guint64)...
var elementLatencyRe = regexp.MustCompile(`element-latency,.*?element=\(string\)([^,]+),.*?time=\(guint64\)(\d+)`)

// parseTracerLog reads a GST_DEBUG_FILE log written by the latency
// tracer and returns per-element latency samples, filtered to the
// given set of element names (empty set = accept all). Only
// element-latency entries are parsed; rusage and other trace events
// are ignored.
func parseTracerLog(path string, track map[string]bool) (map[string][]time.Duration, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return parseTracerStream(f, track)
}

// parseTracerStream is the io.Reader-backed half of parseTracerLog so
// it can be tested with a string reader.
func parseTracerStream(r io.Reader, track map[string]bool) (map[string][]time.Duration, error) {
	out := make(map[string][]time.Duration)
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.Contains(line, "element-latency,") {
			continue
		}
		plain := ansiRe.ReplaceAllString(line, "")
		m := elementLatencyRe.FindStringSubmatch(plain)
		if m == nil {
			continue
		}
		name := m[1]
		if len(track) > 0 && !track[name] {
			continue
		}
		ns, err := strconv.ParseUint(m[2], 10, 64)
		if err != nil {
			continue
		}
		out[name] = append(out[name], time.Duration(ns))
	}
	return out, scanner.Err()
}

// percentileAndMean returns min/max/mean/p50/p90 of the input after
// chronologically trimming trimRatio samples from the head AND the
// tail. Input must be in time order. Returns zeros if too few
// samples survive the trim. The returned sorted slice is for
// callers that want the raw steady-state distribution.
func percentileAndMean(in []time.Duration) (sorted []time.Duration, min, max, mean, p50, p90 time.Duration) {
	n := len(in)
	trim := int(float64(n) * trimRatio)
	if n-2*trim < 1 {
		return nil, 0, 0, 0, 0, 0
	}
	used := in[trim : n-trim]
	sorted = make([]time.Duration, len(used))
	copy(sorted, used)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	pct := func(p float64) time.Duration {
		idx := int(float64(len(sorted)-1) * p)
		return sorted[idx]
	}
	var sum time.Duration
	for _, d := range sorted {
		sum += d
	}
	return sorted, sorted[0], sorted[len(sorted)-1], sum / time.Duration(len(sorted)), pct(0.50), pct(0.90)
}
