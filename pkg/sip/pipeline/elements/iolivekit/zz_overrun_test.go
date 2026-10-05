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

package iolivekit

import (
	"testing"
	"time"

	"github.com/go-gst/go-gst/gst"
)

func TestWatchAudioOutOverruns_CountsLeakyDrops(t *testing.T) {
	pipeline, err := gst.NewPipeline("test-" + t.Name())
	if err != nil {
		t.Fatal("failed to create pipeline:", err)
	}
	src, err := gst.NewElementWithProperties("audiotestsrc", map[string]interface{}{"is-live": true})
	if err != nil {
		t.Fatal("failed to create audiotestsrc:", err)
	}
	queue, err := gst.NewElementWithProperties("queue", map[string]interface{}{
		"max-size-buffers": uint(0),
		"max-size-bytes":   uint(0),
		"max-size-time":    uint(100_000_000),
		"leaky":            int(2),
	})
	if err != nil {
		t.Fatal("failed to create queue:", err)
	}
	slow, err := gst.NewElementWithProperties("identity", map[string]interface{}{"sleep-time": uint(50_000)})
	if err != nil {
		t.Fatal("failed to create identity:", err)
	}
	sink, err := gst.NewElementWithProperties("fakesink", map[string]interface{}{"sync": false})
	if err != nil {
		t.Fatal("failed to create fakesink:", err)
	}
	if err := pipeline.AddMany(src, queue, slow, sink); err != nil {
		t.Fatal("failed to add elements:", err)
	}
	if err := gst.ElementLinkMany(src, queue, slow, sink); err != nil {
		t.Fatal("failed to link elements:", err)
	}

	stats := &audioOutStats{}
	watchAudioOutOverruns(pipeline.Bin, queue, stats)

	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		t.Fatal("failed to set PLAYING:", err)
	}
	time.Sleep(1500 * time.Millisecond)
	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatal("failed to set NULL:", err)
	}

	if n := stats.overruns.Load(); n == 0 {
		t.Fatal("no overrun counted while the consumer was slower than real time")
	} else {
		t.Logf("%d overruns", n)
	}
}
