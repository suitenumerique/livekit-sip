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
			p.Log.Debugw("Dumping pipeline state to dot file")
			count++
			done := make(chan struct{})
			if _, err := glib.IdleAdd(func() {
				if p.Closed() {
					p.Log.Debugw("Pipeline closed, skipping dump")
					close(done)
					return
				}
				pipeline := p.Pipeline()
				if pipeline == nil {
					p.Log.Debugw("Pipeline is nil, skipping dump")
					close(done)
					return
				}
				data := p.Pipeline().DebugBinToDotData(gst.DebugGraphShowAll | gst.DebugGraphShowFullParams)
				filename := fmt.Sprintf("%s/pipeline-%s-%d.dot", p.dumpDir, p.sipCallID, count)
				if err := os.WriteFile(filename, []byte(data), 0644); err != nil {
					p.Log.Errorw("Failed to write pipeline dot file", err, "filename", filename)
				} else {
					p.Log.Infow("Pipeline dot file written", "filename", filename)
				}
				close(done)
			}); err != nil {
				p.Log.Errorw("Failed to add idle function for dumping pipeline", err)
				return
			}
			<-done
			p.Log.Debugw("Pipeline state dumped to dot file")
		}
	}

	if err := p.ensureDumpDir(); err != nil {
		p.Log.Warnw("Pipeline dumping is disabled", err, "dumpDot", p.dumpDot, "dumpDir", p.dumpDir)
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
			p.Log.Infow("Dump directory created", "directory", p.dumpDir)
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
