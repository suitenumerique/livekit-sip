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

package keyframe

import (
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-gst/go-gst/gst"
	"github.com/stretchr/testify/require"

	"github.com/livekit/sip/pkg/sip/pipeline/elements/testutils"
)

func TestMain(m *testing.M) {
	gst.Init(nil)
	os.Exit(m.Run())
}

func decoderSrcPad(t *testing.T) (*gst.Pad, *atomic.Int32) {
	t.Helper()
	pad := gst.NewPad("src", gst.PadDirectionSource)
	require.True(t, pad.SetActive(true))
	requests := &atomic.Int32{}
	pad.AddProbe(gst.PadProbeTypeEventUpstream, func(_ *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		if ev := info.GetEvent(); ev != nil && ev.HasName("GstForceKeyUnit") {
			requests.Add(1)
		}
		return gst.PadProbeOK
	})
	RequestOnBadBuffer(pad)
	return pad, requests
}

func frame(pts time.Duration, flags gst.BufferFlags) *gst.Buffer {
	buf := gst.NewBufferFromBytes(make([]byte, 16))
	buf.SetPresentationTimestamp(gst.ClockTime(pts))
	if flags != 0 {
		buf.SetFlags(flags)
	}
	return buf
}

func TestRequestOnBadBuffer_DecreasingTimestampDoesNotRequest(t *testing.T) {
	defer testutils.AssertNoLeaks(t)
	pad, requests := decoderSrcPad(t)

	for _, pts := range []time.Duration{0, 33, 66, 40, 100, 100, 133, 107} {
		pad.Push(frame(pts*time.Millisecond, 0))
	}
	require.Zero(t, requests.Load())
	require.True(t, pad.SetActive(false))
}

func TestRequestOnBadBuffer_LossRequestsOncePerInterval(t *testing.T) {
	defer testutils.AssertNoLeaks(t)
	pad, requests := decoderSrcPad(t)

	pad.Push(frame(0, 0))
	pad.Push(frame(33*time.Millisecond, gst.BufferFlagDiscont))
	require.EqualValues(t, 1, requests.Load())
	pad.Push(frame(66*time.Millisecond, gst.BufferFlagCorrupted))
	require.EqualValues(t, 1, requests.Load())
	require.True(t, pad.SetActive(false))
}
