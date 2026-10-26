// Copyright 2026 LiveKit, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// 	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package pipeline

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/go-gst/go-gst/gst"
	"github.com/stretchr/testify/require"
)

// When the device switches payload type, the branch of the old payload
// type goes silent while the new one starts. The camera selector must move to
// the new branch even when its first frame comes right after the last frame of
// the old branch.
func TestSipCompositorCameraFollowsNewBranch(t *testing.T) {
	gst.Init(nil)

	pl, err := gst.NewPipeline("selector-switch")
	require.NoError(t, err)
	defer pl.SetState(gst.StateNull)

	comp, err := gst.NewElementWithProperties("sip_compositor", map[string]interface{}{
		"video-width":  uint(64),
		"video-height": uint(64),
		"framerate":    uint(30),
	})
	require.NoError(t, err)
	require.NoError(t, pl.Add(comp))
	comp.Connect("pad-added", func(_ *gst.Element, pad *gst.Pad) {
		if !strings.HasPrefix(pad.GetName(), "src_") {
			return
		}
		sink, err := gst.NewElementWithProperties("fakesink", map[string]interface{}{"sync": false, "async": false})
		if err != nil {
			return
		}
		_ = pl.Add(sink)
		sink.SyncStateWithParent()
		pad.Link(sink.GetStaticPad("sink"))
	})

	branch := func(pt int) (*gst.Element, *gst.Element) {
		src, err := gst.NewElementWithProperties("videotestsrc", map[string]interface{}{"is-live": true})
		require.NoError(t, err)
		caps, err := gst.NewElementWithProperties("capsfilter", map[string]interface{}{
			"caps": gst.NewCapsFromString("video/x-raw,format=I420,width=64,height=64,framerate=30/1"),
		})
		require.NoError(t, err)
		valve, err := gst.NewElementWithProperties("valve", map[string]interface{}{"drop": pt != 96})
		require.NoError(t, err)
		queue, err := gst.NewElementWithProperties("queue", map[string]interface{}{"max-size-buffers": 3, "leaky": 2})
		require.NoError(t, err)
		chain := []*gst.Element{src, caps, valve, queue}
		require.NoError(t, pl.AddMany(chain...))
		require.NoError(t, gst.ElementLinkMany(chain...))
		sink := comp.GetRequestPad(fmt.Sprintf("sink_1_861957632_%d", pt))
		require.NotNil(t, sink)
		require.Equal(t, gst.PadLinkOK, queue.GetStaticPad("src").Link(sink))
		return valve, queue
	}
	oldValve, oldQueue := branch(96)
	newValve, newQueue := branch(126)
	require.NoError(t, pl.SetState(gst.StatePlaying))
	time.Sleep(time.Second)

	_, active, isActive := walkDownstream(oldQueue.GetStaticPad("src"), recvWalkDepth)
	require.True(t, isActive, "the first branch is active, selector=%s", active)

	require.NoError(t, oldValve.SetProperty("drop", true))
	require.NoError(t, newValve.SetProperty("drop", false))

	require.Eventually(t, func() bool {
		_, _, isActive := walkDownstream(newQueue.GetStaticPad("src"), recvWalkDepth)
		return isActive
	}, 6*time.Second, 100*time.Millisecond, "the selector moves to the branch that receives frames")
}
