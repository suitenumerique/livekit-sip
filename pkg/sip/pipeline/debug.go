package pipeline

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/go-gst/go-gst/gst"
)

var ErrPipielineNotRunning = fmt.Errorf("pipeline not running")

var runningTimeRegex = regexp.MustCompile(`running-time=\d+`)

func sanitizeDot(dot string) string {
	return runningTimeRegex.ReplaceAllString(dot, "running-time=XXX")
}

func (p *BasePipeline) debug() (string, string, gst.State, error) {
	p.Mu.Lock()
	defer p.Mu.Unlock()

	state := p.Pipeline.GetCurrentState()
	if state == gst.StateNull {
		return "", "", state, ErrPipielineNotRunning
	}

	dotData := p.Pipeline.DebugBinToDotData(gst.DebugGraphShowCapsDetails)

	data, err := PipelineBranchesAsStrings(p.Pipeline)
	if err != nil {
		return "", "", state, fmt.Errorf("failed to get pipeline branches: %w", err)
	}
	debugOutput := strings.Join(data, "\n")

	return sanitizeDot(dotData), debugOutput, state, nil
}

func (p *BasePipeline) Monitor() {
	name := p.Pipeline.GetName()

	logFile, err := os.Create(fmt.Sprintf("%s_pipeline_debug.log", name))
	if err != nil {
		fmt.Printf("failed to create pipeline log file: %v\n", err)
		return
	}
	liveFile, err := os.Create(fmt.Sprintf("%s_pipeline_live.dot", name))
	if err != nil {
		fmt.Printf("failed to create pipeline live log file: %v\n", err)
		return
	}

	go func() {
		defer logFile.Close()
		defer liveFile.Close()

		prevStr := ""
		prevDot := ""
		prevState := gst.StateNull

		for !p.closed.IsBroken() {
			dotData, pipelineStr, state, err := p.debug()
			if err != nil {
				if err == ErrPipielineNotRunning {
					pipelineStr = "Pipeline not running"
				}
				pipelineStr = fmt.Sprintf("failed to get pipeline string: %v", err)
			}

			if dotData != prevDot {
				prevDot = dotData
				liveFile.Truncate(0)
				liveFile.Seek(0, 0)
				liveFile.WriteString(dotData)
			}

			if pipelineStr != prevStr || dotData != prevDot || prevState != state {
				prevStr = pipelineStr
				prevState = state

				logFile.WriteString(
					fmt.Sprintf("----- %s: %s -----\n%s\n\n", time.Now().Format(time.RFC3339), state.String(), pipelineStr),
				)
			}

			time.Sleep(500 * time.Millisecond)
		}

		logFile.WriteString("----- Pipeline monitor exiting -----\n")
		liveFile.WriteString("----- Pipeline monitor exiting -----\n")
	}()
}

func PipelineBranchesAsStrings(pipe *gst.Pipeline) ([]string, error) {
	sources, err := pipe.GetSourceElements()
	if err != nil {
		return nil, fmt.Errorf("failed to get source elements: %w", err)
	}

	var branches []string
	for _, src := range sources {
		branches = append(branches, walkFromSource(src)...)
	}
	return branches, nil
}

func walkFromSource(start *gst.Element) []string {
	// we keep a per-path visited map to avoid cycles
	visited := map[*gst.Element]bool{}
	return walkElement(start, "", visited)
}

func elementDesc(e *gst.Element) string {
	name := e.GetName()
	factory := ""
	if f := e.GetFactory(); f != nil {
		factory = f.GetName()
	}
	if name != "" {
		return fmt.Sprintf("%s name=%s", factory, name)
	}
	return factory
}

func cloneVisited(m map[*gst.Element]bool) map[*gst.Element]bool {
	out := make(map[*gst.Element]bool, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// depth-first, following src pads
func walkElement(e *gst.Element, prefix string, visited map[*gst.Element]bool) []string {
	visited[e] = true

	desc := elementDesc(e)
	if prefix == "" {
		prefix = desc
	} else {
		prefix = prefix + " !\n\t" + desc
	}

	srcPads, err := e.GetSrcPads()
	if err != nil || len(srcPads) == 0 {
		// no src pads: this is a sink / leaf
		return []string{prefix}
	}

	// collect next elements from linked src pads
	var nextElems []*gst.Element
	for _, p := range srcPads {
		if !p.IsLinked() {
			continue
		}
		peer := p.GetPeer()
		if peer == nil {
			continue
		}
		next := peer.GetParentElement()
		if next == nil {
			continue
		}
		nextElems = append(nextElems, next)
	}

	if len(nextElems) == 0 {
		return []string{prefix}
	}

	// branch if multiple downstream elements (tee / compositor, etc.)
	var out []string
	for _, next := range nextElems {
		if visited[next] {
			// avoid infinite loops in weird graphs
			out = append(out, prefix+" ! <cycle:"+next.GetName()+">")
			continue
		}
		// clone visited so branches don't block each other
		branchVisited := cloneVisited(visited)
		out = append(out, walkElement(next, prefix, branchVisited)...)
	}
	return out
}

func NewDebugView(name string) (*gst.Bin, error) {
	bin := gst.NewBin(name)

	tee, err := gst.NewElement("tee")
	if err != nil {
		return nil, fmt.Errorf("failed to create tee: %w", err)
	}

	queuePass, err := gst.NewElementWithProperties("queue", map[string]interface{}{
		"max-size-buffers": uint(1),
		"leaky":            int(2), // downstream
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create pass queue: %w", err)
	}

	queueView, err := gst.NewElementWithProperties("queue", map[string]interface{}{
		"max-size-buffers": uint(1),
		"leaky":            int(2), // downstream
		"silent":           true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create view queue: %w", err)
	}

	convert, err := gst.NewElement("videoconvert")
	if err != nil {
		return nil, fmt.Errorf("failed to create videoconvert: %w", err)
	}

	sink, err := gst.NewElementWithProperties("glimagesink", map[string]interface{}{
		"sync":  false,
		"async": false, // prevent pipeline hang when unlinked
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create video sink: %w", err)
	}

	if err := bin.AddMany(tee, queuePass, queueView, convert, sink); err != nil {
		return nil, fmt.Errorf("failed to add elements to bin: %w", err)
	}

	if err := gst.ElementLinkMany(queueView, convert, sink); err != nil {
		return nil, fmt.Errorf("failed to link preview branch: %w", err)
	}

	teePadPass := tee.GetRequestPad("src_%u")
	if teePadPass == nil {
		return nil, fmt.Errorf("failed to request passthrough pad from tee")
	}

	queuePassSink := queuePass.GetStaticPad("sink")
	if linkRes := teePadPass.Link(queuePassSink); linkRes != gst.PadLinkOK {
		return nil, fmt.Errorf("failed to link tee to passthrough queue: %s", linkRes)
	}

	teePadView := tee.GetRequestPad("src_%u")
	if teePadView == nil {
		return nil, fmt.Errorf("failed to request preview pad from tee")
	}

	queueViewSink := queueView.GetStaticPad("sink")
	if linkRes := teePadView.Link(queueViewSink); linkRes != gst.PadLinkOK {
		return nil, fmt.Errorf("failed to link tee to preview queue: %s", linkRes)
	}

	teeSink := tee.GetStaticPad("sink")
	ghostSink := gst.NewGhostPad("sink", teeSink)
	if !bin.AddPad(ghostSink.Pad) {
		return nil, fmt.Errorf("failed to add ghost sink pad")
	}

	queuePassSrc := queuePass.GetStaticPad("src")
	ghostSrc := gst.NewGhostPad("src", queuePassSrc)
	if !bin.AddPad(ghostSrc.Pad) {
		return nil, fmt.Errorf("failed to add ghost src pad")
	}

	return bin, nil
}
