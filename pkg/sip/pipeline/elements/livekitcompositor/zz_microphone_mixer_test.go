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

package livekitcompositor

import (
	"testing"
	"time"

	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/audiobus"
)

func within(t *testing.T, what string, d time.Duration, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		fn()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s still blocked after %s", what, d)
	}
}

func TestMicrophoneMixerAcceptsABufferBeforeItsStartTimeIsSelected(t *testing.T) {
	pipeline, err := gst.NewPipeline("mixer-start-time")
	if err != nil {
		t.Fatalf("pipeline: %v", err)
	}
	mixer, err := newMicrophoneMixer(80 * time.Millisecond)
	if err != nil {
		t.Fatalf("mixer: %v", err)
	}
	sink, err := gst.NewElementWithProperties("fakesink", map[string]interface{}{"sync": false})
	if err != nil {
		t.Fatalf("fakesink: %v", err)
	}
	if err := pipeline.AddMany(mixer, sink); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := mixer.Link(sink); err != nil {
		t.Fatalf("link: %v", err)
	}
	if err := pipeline.SetState(gst.StatePaused); err != nil {
		t.Fatalf("paused: %v", err)
	}

	pad := mixer.GetRequestPad("sink_%u")
	if pad == nil {
		t.Fatal("no sink pad on the mixer")
	}
	pad.SendEvent(gst.NewStreamStartEvent("silence"))
	pad.SendEvent(gst.NewCapsEvent(gst.NewCapsFromString(audiobus.Caps)))
	pad.SendEvent(gst.NewSegmentEvent(gst.NewFormattedSegment(gst.FormatTime)))
	buf := gst.NewBufferFromBytes(make([]byte, 1920))
	buf.SetPresentationTimestamp(0)
	buf.SetDuration(gst.ClockTime(20 * time.Millisecond))

	within(t, "first buffer", 3*time.Second, func() { pad.Chain(buf) })
	within(t, "second pad request", 3*time.Second, func() {
		if second := mixer.GetRequestPad("sink_%u"); second != nil {
			mixer.ReleaseRequestPad(second)
		}
	})
	within(t, "latency recalculation", 3*time.Second, func() { pipeline.RecalculateLatency() })

	mixer.ReleaseRequestPad(pad)
	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatalf("null: %v", err)
	}
}
