package pipeline

import (
	"fmt"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"

	"github.com/livekit/sip/pkg/sip/pipeline/elements/livekitbin"
)

const (
	stuckReportTimeout   = 3 * time.Second
	stuckReportMaxItems  = 30
	stuckReportMaxGroups = 15
	stuckReportMaxStack  = 16 << 20
)

var (
	parkedMu sync.Mutex
	parked   [][]*gst.Element
)

func logStuckClose(pipeline *gst.Pipeline, step string) {
	elems := make(chan string, 1)
	go func() {
		elems <- stuckElements(pipeline)
	}()
	var report string
	select {
	case report = <-elems:
	case <-time.After(stuckReportTimeout):
		report = "\nnot_null=timeout"
	}
	pipeline.Log(CAT, gst.LevelError, fmt.Sprintf("Pipeline stuck while closing\nstep=%s%s\ncallbacks=%s", step, report, summarizeCallbacks(allStacks())))
}

func (p *Pipeline) captureElements() []*gst.Element {
	var elems []*gst.Element
	if p.SipIo != nil {
		elems = append(elems, p.SipIo.SipBin)
		p.SipIo.hopMu.Lock()
		for _, h := range p.SipIo.hops {
			elems = append(elems, h.src, h.sink)
		}
		p.SipIo.hopMu.Unlock()
	}
	if p.WebrtcIo != nil {
		elems = append(elems, p.WebrtcIo.LivekitBin)
		p.WebrtcIo.hopMu.Lock()
		for _, h := range p.WebrtcIo.hops {
			elems = append(elems, h.src, h.sink)
		}
		p.WebrtcIo.hopMu.Unlock()
	}
	if p.IOManager != nil {
		elems = append(elems, p.IOManager.SipController, p.IOManager.LivekitController)
	}
	recursive := make(chan []*gst.Element, 1)
	go func() {
		all, _ := p.pipeline.GetElementsRecursive()
		recursive <- all
	}()
	select {
	case all := <-recursive:
		elems = append(elems, all...)
	case <-time.After(stuckReportTimeout):
	}
	out := elems[:0]
	for _, e := range elems {
		if e != nil {
			out = append(out, e)
		}
	}
	return out
}

func (p *Pipeline) disconnectRoom() {
	if p.WebrtcIo == nil || p.WebrtcIo.LivekitBin == nil {
		return
	}
	lk, ok := glib.FromObjectUnsafePrivate(p.WebrtcIo.LivekitBin.Unsafe()).(*livekitbin.LivekitBin)
	if !ok {
		return
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		lk.Close()
	}()
	select {
	case <-done:
	case <-time.After(stuckReportTimeout):
		p.pipeline.Log(CAT, gst.LevelError, "Timeout disconnecting the LiveKit room of a pipeline that failed to close")
	}
}

func parkElements(elems []*gst.Element) int {
	parkedMu.Lock()
	defer parkedMu.Unlock()
	parked = append(parked, elems)
	total := 0
	for _, e := range parked {
		total += len(e)
	}
	return total
}

func stuckElements(pipeline *gst.Pipeline) string {
	ret, _ := pipeline.GetState(gst.VoidPending, 0)
	head := fmt.Sprintf("\npipeline=%s:%s", pipeline.GetCurrentState(), ret)
	elems, err := pipeline.GetElementsRecursive()
	if err != nil {
		return fmt.Sprintf("%s\nnot_null=error %v", head, err)
	}
	var states, pads []string
	for _, e := range elems {
		name := e.GetName()
		if f := e.GetFactory(); f != nil {
			name = f.GetName() + "/" + name
		}
		cur := e.GetCurrentState()
		ret, _ := e.GetState(gst.VoidPending, 0)
		if cur != gst.StateNull || ret == gst.StateChangeAsync || ret == gst.StateChangeFailure {
			states = append(states, fmt.Sprintf("%s:%s:%s", name, cur, ret))
		}
		elemPads, err := e.GetPads()
		if err != nil {
			continue
		}
		for _, pad := range elemPads {
			var flags []string
			if pad.Has(gst.PadFlagBlocked) {
				flags = append(flags, "blocked")
			}
			if pad.Has(gst.PadFlagBlocking) {
				flags = append(flags, "blocking")
			}
			if len(flags) > 0 {
				pads = append(pads, fmt.Sprintf("%s.%s:%s", name, pad.GetName(), strings.Join(flags, "+")))
			}
		}
	}
	return fmt.Sprintf("%s\nnot_null=%s\nblocked_pads=%s", head, limitJoin(states), limitJoin(pads))
}

func limitJoin(items []string) string {
	if len(items) == 0 {
		return "none"
	}
	sort.Strings(items)
	if len(items) > stuckReportMaxItems {
		items = append(items[:stuckReportMaxItems], fmt.Sprintf("+%d", len(items)-stuckReportMaxItems))
	}
	return strings.Join(items, ",")
}

func allStacks() string {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) || len(buf) >= stuckReportMaxStack {
			return string(buf[:n])
		}
		buf = make([]byte, 2*len(buf))
	}
}

func summarizeCallbacks(dump string) string {
	counts := make(map[string]int)
	for _, g := range strings.Split(dump, "\n\n") {
		if !strings.Contains(g, "runtime.cgocallback") {
			continue
		}
		lines := strings.Split(g, "\n")
		state := goroutineState(lines[0])
		if state == "" || state == "running" || state == "runnable" {
			continue
		}
		var innermost string
		var ours []string
		for _, l := range lines[1:] {
			if l == "" || l[0] == '\t' || strings.HasPrefix(l, "created by ") {
				continue
			}
			f := shortFrame(l)
			if f == "" {
				continue
			}
			if innermost == "" {
				innermost = f
			}
			if f != innermost && (strings.Contains(l, "github.com/livekit/sip/") || strings.Contains(l, "github.com/go-gst/")) && len(ours) < 3 {
				ours = append(ours, f)
			}
		}
		if innermost == "" {
			continue
		}
		counts[state+": "+strings.Join(append([]string{innermost}, ours...), " < ")]++
	}
	if len(counts) == 0 {
		return "none"
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if counts[keys[i]] != counts[keys[j]] {
			return counts[keys[i]] > counts[keys[j]]
		}
		return keys[i] < keys[j]
	})
	if len(keys) > stuckReportMaxGroups {
		keys = keys[:stuckReportMaxGroups]
	}
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = fmt.Sprintf("%dx %s", counts[k], k)
	}
	return strings.Join(out, " | ")
}

func goroutineState(header string) string {
	start := strings.Index(header, "[")
	end := strings.LastIndex(header, "]")
	if start < 0 || end <= start {
		return ""
	}
	state := header[start+1 : end]
	if i := strings.Index(state, ","); i >= 0 {
		state = state[:i]
	}
	return state
}

func shortFrame(line string) string {
	if i := strings.LastIndex(line, "("); i > 0 {
		line = line[:i]
	}
	for _, skip := range []string{"runtime.", "sync.", "internal/", "_cgoexp_", "github.com/go-gst/go-gst/gst._Cfunc_"} {
		if strings.HasPrefix(line, skip) {
			return ""
		}
	}
	for _, prefix := range []string{"github.com/livekit/sip/pkg/sip/pipeline/elements/", "github.com/livekit/sip/pkg/sip/", "github.com/go-gst/go-gst/"} {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimPrefix(line, prefix)
		}
	}
	return line
}
