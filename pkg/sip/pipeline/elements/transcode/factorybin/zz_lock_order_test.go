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

package factorybin

import (
	"sync"
	"testing"
	"time"

	"github.com/go-gst/go-gst/gst"
)

func TestFactoryBin_Chained_ConcurrentQueryAndReconfigure(t *testing.T) {
	pipeline, err := gst.NewPipeline("test-" + t.Name())
	if err != nil {
		t.Fatal("failed to create pipeline:", err)
	}

	upstream := newFactoryBin(t, []string{"h264-video", "vp8-video"})
	downstream := newFactoryBin(t, []string{"video-h264", "video-vp8"})
	outFilter, err := gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString(h264RTPCaps),
	})
	if err != nil {
		t.Fatal("failed to create capsfilter:", err)
	}
	sink, err := gst.NewElementWithProperties("fakesink", map[string]interface{}{"sync": false})
	if err != nil {
		t.Fatal("failed to create fakesink:", err)
	}
	if err := pipeline.AddMany(upstream, downstream, outFilter, sink); err != nil {
		t.Fatal("failed to add elements to pipeline:", err)
	}
	if err := gst.ElementLinkMany(upstream, downstream, outFilter, sink); err != nil {
		t.Fatal("failed to link elements:", err)
	}
	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		t.Fatal("failed to set pipeline to PLAYING:", err)
	}

	upstreamSink := upstream.GetStaticPad("sink")
	sendStreamStart(t, upstreamSink)
	if !sendCaps(t, upstreamSink, h264RTPCaps) {
		t.Fatal("RTP caps event was rejected")
	}

	const iterations = 3000
	downstreamSink := downstream.GetStaticPad("sink")
	filterSink := outFilter.GetStaticPad("sink")

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for range iterations {
			filterSink.PeerQueryCaps(nil)
		}
	}()
	go func() {
		defer wg.Done()
		for range iterations {
			downstreamSink.PushEvent(gst.NewReconfigureEvent())
		}
	}()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("caps query and reconfigure across two chained factorybins did not complete: lock order deadlock")
	}

	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatal("failed to set pipeline to NULL:", err)
	}
}
