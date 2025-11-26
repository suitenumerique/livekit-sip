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

func (gp *GstPipeline) debug() (string, string, gst.State, error) {
	gp.mu.Lock()
	defer gp.mu.Unlock()

	state := gp.Pipeline.GetCurrentState()
	if state == gst.StateNull {
		return "", "", state, ErrPipielineNotRunning
	}

	dotData := gp.Pipeline.DebugBinToDotData(gst.DebugGraphShowMediaType)

	data, err := PipelineBranchesAsStrings(gp.Pipeline)
	if err != nil {
		return "", "", state, fmt.Errorf("failed to get pipeline branches: %w", err)
	}
	debugOutput := strings.Join(data, "\n")

	return sanitizeDot(dotData), debugOutput, state, nil
}

func (gp *GstPipeline) Monitor() {
	name := gp.Pipeline.GetName()

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

		for !gp.closed.IsBroken() {
			dotData, pipelineStr, state, err := gp.debug()
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
				//				fmt.Printf("Wrote live pipeline dot data (%d bytes)\n", len(dotData))
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
