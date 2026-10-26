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
	"testing"
	"time"

	"github.com/go-gst/go-gst/gst"
	"github.com/stretchr/testify/require"
)

func TestWalkDownstreamInactiveSelectorBranch(t *testing.T) {
	gst.Init(nil)

	pl, err := gst.NewPipelineFromString(
		"input-selector name=sel ! fakesink sync=false " +
			"audiotestsrc name=src0 is-live=true ! queue name=q0 ! sel.sink_0 " +
			"audiotestsrc name=src1 is-live=true ! queue name=q1 ! sel.sink_1")
	require.NoError(t, err)
	defer pl.SetState(gst.StateNull)

	sel, err := pl.GetElementByName("sel")
	require.NoError(t, err)
	require.NoError(t, sel.SetProperty("active-pad", sel.GetStaticPad("sink_0")))
	require.NoError(t, pl.SetState(gst.StatePlaying))
	time.Sleep(time.Second)

	// The input-selector drops the buffers of an inactive pad and returns ok:
	// the inactive branch keeps running.
	src1, err := pl.GetElementByName("src1")
	require.NoError(t, err)
	path, active, isActive := walkDownstream(src1.GetStaticPad("src"), recvWalkDepth)
	require.Equal(t, []string{"src1.src=ok", "q1.src=ok", "sel.sink_1"}, path)
	require.Equal(t, "sink_0", active)
	require.False(t, isActive)
	require.Equal(t, gst.TaskStarted, src1.GetStaticPad("src").GetTaskState())

	src0, err := pl.GetElementByName("src0")
	require.NoError(t, err)
	path, active, isActive = walkDownstream(src0.GetStaticPad("src"), recvWalkDepth)
	require.Equal(t, []string{"src0.src=ok", "q0.src=ok", "sel.sink_0"}, path)
	require.Equal(t, "sink_0", active)
	require.True(t, isActive)
}

func TestWalkDownstreamStoppedBranch(t *testing.T) {
	gst.Init(nil)

	pl, err := gst.NewPipelineFromString("audiotestsrc name=src is-live=true ! queue name=q")
	require.NoError(t, err)
	defer pl.SetState(gst.StateNull)
	require.NoError(t, pl.SetState(gst.StatePlaying))

	src, err := pl.GetElementByName("src")
	require.NoError(t, err)
	pad := src.GetStaticPad("src")
	require.Eventually(t, func() bool {
		return pad.GetTaskState() == gst.TaskPaused
	}, 5*time.Second, 50*time.Millisecond, "not-linked further down stops the source")

	path, active, isActive := walkDownstream(pad, recvWalkDepth)
	require.Len(t, path, 3)
	require.NotEqual(t, "src.src=ok", path[0])
	require.Equal(t, []string{"q.src=not-linked", "unlinked"}, path[1:])
	require.Empty(t, active)
	require.False(t, isActive)
}

func TestWalkDownstreamThroughBins(t *testing.T) {
	gst.Init(nil)

	pl, err := gst.NewPipelineFromString(
		"audiotestsrc name=src is-live=true ! bin.( name=b identity name=inner ! queue name=q ) ! input-selector name=sel ! fakesink sync=false")
	require.NoError(t, err)
	defer pl.SetState(gst.StateNull)
	require.NoError(t, pl.SetState(gst.StatePlaying))

	src, err := pl.GetElementByName("src")
	require.NoError(t, err)
	var path []string
	var isActive bool
	require.Eventually(t, func() bool {
		path, _, isActive = walkDownstream(src.GetStaticPad("src"), recvWalkDepth)
		return len(path) > 0 && path[0] == "src.src=ok"
	}, 5*time.Second, 50*time.Millisecond)

	require.Len(t, path, 5)
	require.Equal(t, []string{"src.src=ok", "inner.src=ok", "q.src=ok"}, path[:3])
	require.Regexp(t, `^b\.[^=]+=ok$`, path[3])
	require.Equal(t, "sel.sink_0", path[4])
	require.True(t, isActive)
}
