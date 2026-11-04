// Copyright 2026 LiveKit, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package pipeline

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime/pprof"
	"time"
)

const (
	loopBeatPeriod = time.Second
	loopSlowAfter  = 5 * time.Second
	loopStallAfter = 30 * time.Second
	loopMaxTick    = 2 * loopBeatPeriod
	loopMaxDumps   = 5
)

type loopEvent int

const (
	loopEventNone loopEvent = iota
	loopEventSlow
	loopEventStalled
	loopEventRecovered
)

type loopWatch struct {
	beats   uint64
	armed   bool
	stalled bool
	silent  time.Duration
}

func (w *loopWatch) tick(elapsed time.Duration, beats uint64) (loopEvent, time.Duration) {
	if beats != w.beats {
		silent, stalled, armed := w.silent, w.stalled, w.armed
		w.beats, w.silent, w.stalled, w.armed = beats, 0, false, true
		switch {
		case !armed:
			return loopEventNone, 0
		case stalled:
			return loopEventRecovered, silent
		case silent >= loopSlowAfter:
			return loopEventSlow, silent
		}
		return loopEventNone, silent
	}
	if !w.armed {
		return loopEventNone, 0
	}
	w.silent += min(elapsed, loopMaxTick)
	if !w.stalled && w.silent >= loopStallAfter {
		w.stalled = true
		return loopEventStalled, w.silent
	}
	return loopEventNone, w.silent
}

func writeGoroutineDump(dir string, at time.Time) (string, error) {
	if dir == "" {
		dir = os.TempDir()
	}
	dir = os.ExpandEnv(dir)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", fmt.Errorf("failed to create dump directory: %w", err)
	}
	path := filepath.Join(dir, "main-loop-stall-"+at.UTC().Format("20060102-150405")+".txt")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return "", fmt.Errorf("failed to create dump file: %w", err)
	}
	defer f.Close()
	if err := pprof.Lookup("goroutine").WriteTo(f, 2); err != nil {
		return "", fmt.Errorf("failed to write dump file: %w", err)
	}
	return path, nil
}
