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

package livekitcompositor_test

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/testutils"
)

// screenshareOutput records the caps and the top-left luma of the frames
// leaving the compositor src_3 pad.
type screenshareOutput struct {
	minWidth atomic.Int32
	width    atomic.Int32
	height   atomic.Int32
	luma     atomic.Int32
}

func watchScreenshareOutput(t *testing.T, compositor *gst.Element) *screenshareOutput {
	t.Helper()
	out := &screenshareOutput{}
	out.minWidth.Store(1 << 30)
	pad := screenshareSrcPad(compositor)
	if pad == nil {
		t.Fatal("compositor src_3 pad not found")
	}
	i420 := false
	pad.AddProbe(gst.PadProbeTypeBuffer|gst.PadProbeTypeEventDownstream, func(_ *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		if info.Type()&gst.PadProbeTypeEventDownstream != 0 {
			ev := info.GetEvent()
			if ev == nil {
				return gst.PadProbeOK
			}
			if ev.Type() != gst.EventTypeCaps {
				return gst.PadProbeOK
			}
			st := ev.ParseCaps().GetStructureAt(0)
			w, _ := st.GetValue("width")
			h, _ := st.GetValue("height")
			f, _ := st.GetValue("format")
			width, _ := w.(int)
			height, _ := h.(int)
			format, _ := f.(string)
			i420 = format == "I420"
			out.width.Store(int32(width))
			out.height.Store(int32(height))
			if int32(width) < out.minWidth.Load() {
				out.minWidth.Store(int32(width))
			}
			return gst.PadProbeOK
		}
		buf := info.GetBuffer()
		if buf == nil || !i420 {
			return gst.PadProbeOK
		}
		mapped := buf.Map(gst.MapRead)
		if mapped != nil {
			if data := mapped.Bytes(); len(data) > 0 {
				out.luma.Store(int32(data[0]))
			}
			buf.Unmap()
		}
		return gst.PadProbeOK
	})
	return out
}

func runMainLoop(t *testing.T) func() {
	t.Helper()
	loop := glib.NewMainLoop(glib.MainContextDefault(), false)
	go loop.Run()
	return loop.Quit
}

// compositorElementCount counts the elements of a factory inside the compositor bin.
func compositorElementCount(t *testing.T, compositor *gst.Element, factory string) int {
	t.Helper()
	elements, err := gst.ToGstBin(compositor).GetElements()
	if err != nil {
		t.Fatalf("failed to list compositor elements: %v", err)
	}
	n := 0
	for _, e := range elements {
		if f := e.GetFactory(); f != nil && f.GetName() == factory {
			n++
		}
	}
	return n
}

// fallbackswitchSinkCount counts the sink pads of the screenshare fallbackswitch.
func fallbackswitchSinkCount(t *testing.T, compositor *gst.Element) int {
	t.Helper()
	elements, err := gst.ToGstBin(compositor).GetElements()
	if err != nil {
		t.Fatalf("failed to list compositor elements: %v", err)
	}
	for _, e := range elements {
		if f := e.GetFactory(); f != nil && f.GetName() == "fallbackswitch" {
			pads, err := e.GetSinkPads()
			if err != nil {
				t.Fatalf("failed to list fallbackswitch sink pads: %v", err)
			}
			return len(pads)
		}
	}
	return -1
}

// The SFU sends 8x8 black keyframes when a published track stops: the output
// keeps the last presenter frame and never takes their size.
func TestScreenshare_BlankFramesAreDropped(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, compositor := newPipeline(t, "test-screenshare-blank")
	alice := attachScreensharePresenter(t, pipeline, compositor, "alice", 3031, 2)
	sink := newFakeSink(t, pipeline)
	linkCompositorScreenshareOut(t, compositor, sink.Convert)
	out := watchScreenshareOutput(t, compositor)

	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		t.Fatalf("failed to set PLAYING: %v", err)
	}
	time.Sleep(time.Second)
	presenterLuma := out.luma.Load()
	t.Logf("presenter output %dx%d luma=%d", out.width.Load(), out.height.Load(), presenterLuma)

	alice.Caps.SetProperty("caps", gst.NewCapsFromString("video/x-raw,format=I420,width=8,height=8,framerate=15/1"))
	before := sink.Count.Load()
	time.Sleep(time.Second)

	if w := out.minWidth.Load(); w <= 16 {
		t.Fatalf("a blank frame reached the screenshare output: width=%d", w)
	}
	if out.width.Load() != 320 || out.height.Load() != 240 {
		t.Fatalf("output size changed to %dx%d", out.width.Load(), out.height.Load())
	}
	if luma := out.luma.Load(); luma != presenterLuma {
		t.Fatalf("output no longer shows the last presenter frame: luma=%d, presenter=%d", luma, presenterLuma)
	}
	if produced := sink.Count.Load() - before; produced == 0 {
		t.Fatal("no screenshare frames while the presenter sent blank frames")
	}

	alice.release(compositor)
	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatalf("failed to set NULL: %v", err)
	}
	alice.cleanup()
	sink.cleanup()
	compositor = nil
	pipeline = nil
	_, _ = compositor, pipeline
}

// Once the last presenter has left for more than 500 ms, the output shows the
// end of screenshare message at the presenter size until the grace period ends.
func TestScreenshare_EndMessageAfterLastPresenter(t *testing.T) {
	defer testutils.AssertNoLeaks(t)
	stop := runMainLoop(t)
	defer stop()

	pipeline, compositor := newPipeline(t, "test-screenshare-end-message")
	alice := attachScreensharePresenter(t, pipeline, compositor, "alice", 3041, 2)
	sink := newFakeSink(t, pipeline)
	linkCompositorScreenshareOut(t, compositor, sink.Convert)
	out := watchScreenshareOutput(t, compositor)

	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		t.Fatalf("failed to set PLAYING: %v", err)
	}
	time.Sleep(time.Second)
	presenterLuma := out.luma.Load()

	alice.release(compositor)
	released := time.Now()
	time.Sleep(300 * time.Millisecond)
	if n := compositorElementCount(t, compositor, "videotestsrc"); n != 0 {
		t.Fatalf("end of screenshare message shown before 500 ms (%d sources)", n)
	}
	if luma := out.luma.Load(); luma != presenterLuma {
		t.Fatalf("last presenter frame not kept before the message: luma=%d, presenter=%d", luma, presenterLuma)
	}

	time.Sleep(time.Until(released.Add(900 * time.Millisecond)))
	if n := compositorElementCount(t, compositor, "videotestsrc"); n != 1 {
		t.Fatalf("expected the end of screenshare message source, got %d", n)
	}
	if n := fallbackswitchSinkCount(t, compositor); n != 1 {
		t.Fatalf("expected only the message pad on the fallbackswitch, got %d", n)
	}
	if out.width.Load() != 320 || out.height.Load() != 240 {
		t.Fatalf("message size %dx%d, expected the presenter size 320x240", out.width.Load(), out.height.Load())
	}
	if luma := out.luma.Load(); luma >= 60 {
		t.Fatalf("output is not the dark message background: luma=%d", luma)
	}
	t.Logf("message output %dx%d luma=%d", out.width.Load(), out.height.Load(), out.luma.Load())

	time.Sleep(time.Until(released.Add(3500 * time.Millisecond)))
	if screenshareSrcPad(compositor) != nil {
		t.Fatal("src_3 still present after the grace period")
	}
	if n := compositorElementCount(t, compositor, "videotestsrc"); n != 0 {
		t.Fatalf("end of screenshare message left after teardown (%d sources)", n)
	}

	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatalf("failed to set NULL: %v", err)
	}
	alice.cleanup()
	sink.cleanup()
	compositor = nil
	pipeline = nil
	_, _ = compositor, pipeline
}

// A presenter that arrives while the message is shown replaces it and keeps
// the screenshare output.
func TestScreenshare_NewPresenterReplacesEndMessage(t *testing.T) {
	defer testutils.AssertNoLeaks(t)
	stop := runMainLoop(t)
	defer stop()

	pipeline, compositor := newPipeline(t, "test-screenshare-message-switch")
	alice := attachScreensharePresenter(t, pipeline, compositor, "alice", 3051, 2)
	sink := newFakeSink(t, pipeline)
	linkCompositorScreenshareOut(t, compositor, sink.Convert)
	out := watchScreenshareOutput(t, compositor)

	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		t.Fatalf("failed to set PLAYING: %v", err)
	}
	time.Sleep(time.Second)

	alice.release(compositor)
	time.Sleep(900 * time.Millisecond)
	if n := compositorElementCount(t, compositor, "videotestsrc"); n != 1 {
		t.Fatalf("expected the end of screenshare message source, got %d", n)
	}

	bob := attachScreensharePresenter(t, pipeline, compositor, "bob", 3052, 2)
	time.Sleep(time.Second)
	if n := compositorElementCount(t, compositor, "videotestsrc"); n != 0 {
		t.Fatalf("end of screenshare message still attached with a presenter (%d sources)", n)
	}
	if n := fallbackswitchSinkCount(t, compositor); n != 1 {
		t.Fatalf("expected only the presenter pad on the fallbackswitch, got %d", n)
	}
	if luma := out.luma.Load(); luma < 80 {
		t.Fatalf("output does not show the new presenter: luma=%d", luma)
	}

	time.Sleep(3 * time.Second)
	if screenshareSrcPad(compositor) == nil {
		t.Fatal("src_3 torn down although a presenter is active")
	}

	bob.release(compositor)
	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatalf("failed to set NULL: %v", err)
	}
	alice.cleanup()
	bob.cleanup()
	sink.cleanup()
	compositor = nil
	pipeline = nil
	_, _ = compositor, pipeline
}
