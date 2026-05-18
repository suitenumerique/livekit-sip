package benchmarks

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/h264rtppaybin"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/av1video"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/factorybin"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/h264video"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/videoav1"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/videoh264"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/videovp8"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/videovp9"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/vp8video"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/vp9video"
)

// initGStreamer initialises GStreamer and registers all custom elements.
// Called by the runner and child processes but NOT by the orchestrator
// (which must never touch GStreamer/CUDA so re-exec'd children start
// with a clean CUDA context).
func initGStreamer() {
	glib.SetEnv("GST_DEBUG_DUMP_DOT_DIR", DotDir, true)
	gst.Init(nil)
	h264rtppaybin.Register()
	h264rtppaybin.Register()
	factorybin.Register()
	vp9video.Register()
	videovp9.Register()
	h264video.Register()
	videoh264.Register()
	vp8video.Register()
	videovp8.Register()
	av1video.Register()
	videoav1.Register()
}

func TestMain(m *testing.M) {
	switch os.Getenv("GSTBENCH_ROLE") {
	case "runner":
		runtime.LockOSThread()
		initGStreamer()
		runRunner()
	case "child":
		runtime.LockOSThread()
		initGStreamer()
		runChild()
	default:
		if err := os.MkdirAll("testdata", 0755); err != nil {
			panic(err)
		}
		if err := os.MkdirAll(DotDir, 0755); err != nil {
			panic(err)
		}
		if entries, err := os.ReadDir(DotDir); err == nil {
			for _, e := range entries {
				_ = os.Remove(filepath.Join(DotDir, e.Name()))
			}
		}
		os.Exit(m.Run())
	}
}

var resolutionPairs = []struct {
	srcW, srcH int
	dstW, dstH int
}{
	{854, 480, 854, 480},
	{1280, 720, 854, 480},
	{1280, 720, 1280, 720},
	{1920, 1080, 854, 480},
	{1920, 1080, 1280, 720},
	{1920, 1080, 1920, 1080},
}

var elementsUnderTest = []Element{
	vp9video.Test(),
	videovp9.Test(),
	h264video.Test(),
	videoh264.Test(),
	vp8video.Test(),
	videovp8.Test(),
	av1video.Test(),
	videoav1.Test(),
}

func TestAllElements(t *testing.T) {
	const (
		fps        = 24
		numBuffers = 240
	)

	var allResults []Result
	for _, elem := range elementsUnderTest {
		elem := elem
		t.Run(elem.Name(), func(t *testing.T) {
			for _, p := range resolutionPairs {
				p := p
				name := fmt.Sprintf("%dx%d_to_%dx%d", p.srcW, p.srcH, p.dstW, p.dstH)
				t.Run(name, func(t *testing.T) {
					r := Run(t, elem, Config{
						SourceWidth:  p.srcW,
						SourceHeight: p.srcH,
						SourceFPS:    fps,
						NumBuffers:   numBuffers,
						TargetWidth:  p.dstW,
						TargetHeight: p.dstH,
					})
					allResults = append(allResults, r)
				})
			}
		})
	}

	if len(allResults) == 0 {
		return
	}
	md := FormatMarkdown(allResults)
	t.Log("\n" + md)
	if err := os.WriteFile("testdata/results.md", []byte(md), 0644); err != nil {
		t.Logf("write results.md: %v", err)
	}
	if err := os.WriteFile("testdata/results.csv", []byte(FormatCSV(allResults)), 0644); err != nil {
		t.Logf("write results.csv: %v", err)
	}
}
