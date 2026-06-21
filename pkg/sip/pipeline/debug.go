package pipeline

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
)

func (p *Pipeline) DumpDotLoop() error {
	ticker := time.NewTicker(500 * time.Millisecond)

	dump := false
	mu := sync.Mutex{}
	count := 0

	onDumpCH := func() {
		mu.Lock()
		defer mu.Unlock()
		dump = true
	}

	dumpPipeline := func() {
		mu.Lock()
		defer mu.Unlock()
		if dump {
			dump = false
			// Logged object-less (CAT.Log) rather than on p.pipeline: this runs
			// asynchronously and p.pipeline may already be nil during shutdown.
			CAT.Log(gst.LevelDebug, fmt.Sprintf("Dumping pipeline state to dot file\ncallID=%s", p.sipCallID))
			count++
			done := make(chan struct{})
			if _, err := glib.IdleAdd(func() {
				if p.Closed() {
					CAT.Log(gst.LevelDebug, fmt.Sprintf("Pipeline closed, skipping dump\ncallID=%s", p.sipCallID))
					close(done)
					return
				}
				pipeline := p.Pipeline()
				if pipeline == nil {
					CAT.Log(gst.LevelDebug, fmt.Sprintf("Pipeline is nil, skipping dump\ncallID=%s", p.sipCallID))
					close(done)
					return
				}
				data := pipeline.DebugBinToDotData(gst.DebugGraphShowAll | gst.DebugGraphShowFullParams)
				filename := fmt.Sprintf("%s/pipeline-%s-%d.dot", p.dumpDir, p.sipCallID, count)
				if err := os.WriteFile(filename, []byte(data), 0644); err != nil {
					pipeline.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to write pipeline dot file\nerr=%v\nfilename=%s", err, filename))
				} else {
					pipeline.Log(CAT, gst.LevelInfo, fmt.Sprintf("Pipeline dot file written\nfilename=%s", filename))
				}
				close(done)
			}); err != nil {
				CAT.Log(gst.LevelError, fmt.Sprintf("Failed to add idle function for dumping pipeline\ncallID=%s\nerr=%v", p.sipCallID, err))
				return
			}
			<-done
			CAT.Log(gst.LevelDebug, fmt.Sprintf("Pipeline state dumped to dot file\ncallID=%s", p.sipCallID))
		}
	}

	if err := p.ensureDumpDir(); err != nil {
		CAT.Log(gst.LevelWarning, fmt.Sprintf("Pipeline dumping is disabled\ndumpDot=%v\ndumpDir=%s\nerr=%v", p.dumpDot, p.dumpDir, err))
		dumpPipeline = func() {}
	}

	go func() {
		for {
			select {
			case <-p.closed.Watch():
				return
			case now := <-p.dumpCH:
				onDumpCH()
				if now {
					dumpPipeline()
				}
			case <-ticker.C:
				dumpPipeline()
			}
		}
	}()

	return nil
}

func (p *Pipeline) ensureDumpDir() error {
	if !p.dumpDot {
		return errors.New("pipeline dumping is disabled by configuration")
	}
	if p.dumpDir == "" {
		return errors.New("pipeline dump directory is not configured")
	}

	p.dumpDir = os.ExpandEnv(p.dumpDir)

	if strings.HasPrefix(p.dumpDir, "~/") {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("failed to get user home directory for dump directory: %w", err)
		}
		p.dumpDir = filepath.Join(homeDir, p.dumpDir[2:])
	}

	var dir os.FileInfo
	dir, err := os.Stat(p.dumpDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			if err := os.MkdirAll(p.dumpDir, 0755); err != nil {
				return fmt.Errorf("pipeline dump directory does not exist and failed to create it: %w", err)
			}
			CAT.Log(gst.LevelInfo, fmt.Sprintf("Dump directory created\ndirectory=%s", p.dumpDir))
			dir, err = os.Stat(p.dumpDir)
			if err != nil {
				return fmt.Errorf("failed to stat dump directory after creating it, disabling pipeline dumping: %w", err)
			}
		} else {
			return fmt.Errorf("failed to stat pipeline dump directory: %w", err)
		}
	}
	if dir == nil || !dir.IsDir() {
		return errors.New("pipeline dump directory is not a directory")
	}

	absPath, err := filepath.Abs(p.dumpDir)
	if err != nil {
		return fmt.Errorf("failed to get absolute path for dump directory: %w", err)
	}
	p.dumpDir = absPath

	return nil
}
