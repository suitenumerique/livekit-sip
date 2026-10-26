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

package threadinfo

import (
	"os"
	"testing"
	"time"

	"github.com/go-gst/go-gst/gst"
	"github.com/stretchr/testify/require"
)

func TestParseStat(t *testing.T) {
	th, ok := parseStat(4242, "4242 (udpsrc17:src) S 1 1 1 0 -1 4194368 120 0 0 0 250 50 0 0 20 0 30 0 1234 0 0")
	require.True(t, ok)
	require.Equal(t, "udpsrc17:src", th.Name)
	require.Equal(t, "S", th.State)
	require.Equal(t, 3*time.Second, th.CPU)

	th, ok = parseStat(7, "7 (a (b) c) R 1 1 1 0 -1 0 0 0 0 0 1 1 0 0")
	require.True(t, ok)
	require.Equal(t, "a (b) c", th.Name)
	require.Equal(t, "R", th.State)

	_, ok = parseStat(1, "garbage")
	require.False(t, ok)
}

func TestPadTaskName(t *testing.T) {
	require.Equal(t, "udpsrc17:src", PadTaskName("udpsrc17", "src"))
	require.Equal(t, "rtpjitterbuffer", PadTaskName("rtpjitterbuffer12", "src"))
}

func TestStreamingThreadIsNamedAfterItsPad(t *testing.T) {
	if _, err := os.Stat(taskDir); err != nil {
		t.Skip("no /proc/self/task")
	}
	gst.Init(nil)
	pipeline, err := gst.NewPipelineFromString("audiotestsrc is-live=true name=tsrc ! fakesink sync=true")
	require.NoError(t, err)
	defer pipeline.SetState(gst.StateNull)
	require.NoError(t, pipeline.SetState(gst.StatePlaying))

	name := PadTaskName("tsrc", "src")
	var before, after Snapshot
	require.Eventually(t, func() bool {
		snap, err := Take()
		require.NoError(t, err)
		before = snap
		return len(snap[name]) == 1
	}, 5*time.Second, 50*time.Millisecond)
	time.Sleep(200 * time.Millisecond)
	after, err = Take()
	require.NoError(t, err)

	desc := after.Describe(name, before)
	require.Regexp(t, `^[RS]`, desc)
	require.Equal(t, "no thread", after.Describe("nothing:src", before))
}
