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
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

func TestCoalescedRun_RunsAfterATrigger(t *testing.T) {
	var c coalescedRun
	done := make(chan struct{})
	c.trigger(func() { close(done) })
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("not run")
	}
}

func TestCoalescedRun_StuckRunStaysAlone(t *testing.T) {
	var c coalescedRun
	release := make(chan struct{})
	started := make(chan struct{})
	var runs atomic.Int32
	f := func() {
		if runs.Add(1) == 1 {
			close(started)
		}
		<-release
	}
	c.trigger(f)
	<-started
	before := runtime.NumGoroutine()
	for range 1000 {
		c.trigger(f)
	}
	time.Sleep(50 * time.Millisecond)
	if after := runtime.NumGoroutine(); after > before {
		t.Errorf("goroutines went from %d to %d behind a stuck run", before, after)
	}
	if got := runs.Load(); got != 1 {
		t.Errorf("%d concurrent runs, want 1", got)
	}
	close(release)
	deadline := time.Now().Add(time.Second)
	for runs.Load() != 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	if got := runs.Load(); got != 2 {
		t.Errorf("%d runs after release, want the stuck one plus one for all the triggers", got)
	}
}

func TestCoalescedRun_NoTriggerIsLost(t *testing.T) {
	var c coalescedRun
	var runs, active, overlaps atomic.Int32
	f := func() {
		if active.Add(1) > 1 {
			overlaps.Add(1)
		}
		runs.Add(1)
		active.Add(-1)
	}
	for range 100000 {
		before := runs.Load()
		c.trigger(f)
		deadline := time.Now().Add(time.Second)
		for runs.Load() == before && time.Now().Before(deadline) {
			runtime.Gosched()
		}
		if runs.Load() == before {
			t.Fatal("trigger lost")
		}
	}
	if overlaps.Load() != 0 {
		t.Errorf("%d overlapping runs", overlaps.Load())
	}
}
