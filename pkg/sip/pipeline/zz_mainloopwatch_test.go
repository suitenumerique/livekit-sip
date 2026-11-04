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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func tickSilently(w *loopWatch, beats uint64, n int) (events []loopEvent) {
	for range n {
		if event, _ := w.tick(loopBeatPeriod, beats); event != loopEventNone {
			events = append(events, event)
		}
	}
	return events
}

func TestLoopWatch_NothingBeforeTheFirstBeat(t *testing.T) {
	var w loopWatch
	if events := tickSilently(&w, 0, 120); len(events) != 0 {
		t.Errorf("events %v before the first beat", events)
	}
	if event, _ := w.tick(loopBeatPeriod, 1); event != loopEventNone {
		t.Errorf("first beat gave event %v", event)
	}
}

func TestLoopWatch_JitterIsSilent(t *testing.T) {
	var w loopWatch
	beats := uint64(1)
	w.tick(loopBeatPeriod, beats)
	for range 100 {
		if events := tickSilently(&w, beats, 3); len(events) != 0 {
			t.Fatalf("events %v after 3 s without a beat", events)
		}
		beats++
		if event, _ := w.tick(loopBeatPeriod, beats); event != loopEventNone {
			t.Fatalf("event %v when the beat returned after 3 s", event)
		}
	}
}

func TestLoopWatch_ShortBlockIsReportedWhenTheBeatReturns(t *testing.T) {
	var w loopWatch
	w.tick(loopBeatPeriod, 1)
	if events := tickSilently(&w, 1, 10); len(events) != 0 {
		t.Fatalf("events %v during a 10 s block", events)
	}
	event, silent := w.tick(loopBeatPeriod, 2)
	if event != loopEventSlow || silent != 10*time.Second {
		t.Errorf("got event %v after %v, want a slow event after 10 s", event, silent)
	}
	if event, _ := w.tick(loopBeatPeriod, 3); event != loopEventNone {
		t.Errorf("event %v on the next beat", event)
	}
}

func TestLoopWatch_StallFiresOnceThenRecovers(t *testing.T) {
	var w loopWatch
	w.tick(loopBeatPeriod, 1)
	events := tickSilently(&w, 1, 120)
	if len(events) != 1 || events[0] != loopEventStalled {
		t.Fatalf("events %v during a 120 s block, want one stall", events)
	}
	event, silent := w.tick(loopBeatPeriod, 2)
	if event != loopEventRecovered || silent != 120*time.Second {
		t.Errorf("got event %v after %v, want a recovery after 120 s", event, silent)
	}
	events = tickSilently(&w, 2, 40)
	if len(events) != 1 || events[0] != loopEventStalled {
		t.Errorf("events %v during a second block, want a new stall", events)
	}
}

func TestLoopWatch_StallAtTheThreshold(t *testing.T) {
	var w loopWatch
	w.tick(loopBeatPeriod, 1)
	if events := tickSilently(&w, 1, int(loopStallAfter/loopBeatPeriod)-1); len(events) != 0 {
		t.Fatalf("events %v before the threshold", events)
	}
	if event, silent := w.tick(loopBeatPeriod, 1); event != loopEventStalled || silent != loopStallAfter {
		t.Errorf("got event %v after %v, want a stall at %v", event, silent, loopStallAfter)
	}
}

func TestLoopWatch_PausedProcessIsNotAStall(t *testing.T) {
	var w loopWatch
	w.tick(loopBeatPeriod, 1)
	if event, silent := w.tick(time.Hour, 1); event != loopEventNone || silent != loopMaxTick {
		t.Errorf("got event %v and %v silent after a one hour pause, want none and %v", event, silent, loopMaxTick)
	}
	if event, _ := w.tick(loopBeatPeriod, 2); event != loopEventNone {
		t.Errorf("event %v when the beat returned after the pause", event)
	}
}

func TestWriteGoroutineDump(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "missing", "dir")
	at := time.Date(2026, 11, 2, 14, 21, 27, 0, time.FixedZone("Paris", 3600))
	path, err := writeGoroutineDump(dir, at)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "main-loop-stall-20261102-132127.txt"); path != want {
		t.Errorf("path %s, want %s", path, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("mode %o, want 600", mode)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "TestWriteGoroutineDump") {
		t.Error("dump does not contain the stack of this test")
	}
}

func TestWriteGoroutineDump_UnusableDirectory(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if path, err := writeGoroutineDump(file, time.Now()); err == nil {
		t.Errorf("no error, dump written to %s", path)
	}
}
