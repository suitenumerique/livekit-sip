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
	"strings"
	"testing"
	"time"

	"github.com/go-gst/go-gst/gst"
	"github.com/stretchr/testify/require"
)

const closeDiagDump = `goroutine 12 [sync.Mutex.Lock, 2 minutes]:
sync.runtime_SemacquireMutex(0x1?, 0x0?, 0x1?)
	/usr/local/go/src/runtime/sema.go:95 +0x25
sync.(*Mutex).Lock(...)
	/usr/local/go/src/sync/mutex.go:46
github.com/livekit/sip/pkg/sip/pipeline/elements/sipbin.(*SipTrack).settleContinuity(0x1a47, 0x2, 0x3)
	/workspace/pkg/sip/pipeline/elements/sipbin/continuity.go:230 +0x45
github.com/livekit/sip/pkg/sip/pipeline/elements/sipbin.(*SipTrack).watchContinuity.func1(0x1, 0x2)
	/workspace/pkg/sip/pipeline/elements/sipbin/continuity.go:197 +0x45
github.com/go-gst/go-gst/gst.goPadProbeFunc(0x1, 0x2, 0x3)
	/go/pkg/mod/github.com/go-gst/go-gst/gst/cgo_exports.go:40 +0x45
_cgoexp_bafcf5c55511_goPadProbeFunc(0x7db3)
	_cgo_gotypes.go:18691 +0x45
runtime.cgocallbackg1(0x14cecc0, 0x7db3, 0x0)
	/usr/local/go/src/runtime/cgocall.go:466 +0x2e5
runtime.cgocallbackg(0x14cecc0, 0x7db3, 0x0)
	/usr/local/go/src/runtime/cgocall.go:362 +0x130
runtime.cgocallback(0x0, 0x0, 0x0)
	/usr/local/go/src/runtime/asm_amd64.s:1160 +0xcc

goroutine 13 [select]:
github.com/pion/transport/v3/packetio.(*Buffer).Read(0x1, {0x2, 0x3, 0x4})
	/go/pkg/mod/github.com/pion/transport/v3/packetio/buffer.go:260 +0x45
github.com/livekit/sip/pkg/sip/pipeline/elements/livekitbin/livekittracks.(*SrcTrackRtp).Fill(0x1, 0x2)
	/workspace/pkg/sip/pipeline/elements/livekitbin/livekittracks/src_track_rtp.go:235 +0x45
github.com/go-gst/go-gst/gst/base.goGstBaseSrcFill(0x1, 0x2)
	/go/pkg/mod/github.com/go-gst/go-gst/gst/base/gst_base_src_exports.go:295 +0x45
runtime.cgocallback(0x0, 0x0, 0x0)
	/usr/local/go/src/runtime/asm_amd64.s:1160 +0xcc

goroutine 14 [select]:
github.com/pion/transport/v3/packetio.(*Buffer).Read(0x1, {0x2, 0x3, 0x4})
	/go/pkg/mod/github.com/pion/transport/v3/packetio/buffer.go:260 +0x45
github.com/livekit/sip/pkg/sip/pipeline/elements/livekitbin/livekittracks.(*SrcTrackRtp).Fill(0x1, 0x2)
	/workspace/pkg/sip/pipeline/elements/livekitbin/livekittracks/src_track_rtp.go:235 +0x45
github.com/go-gst/go-gst/gst/base.goGstBaseSrcFill(0x1, 0x2)
	/go/pkg/mod/github.com/go-gst/go-gst/gst/base/gst_base_src_exports.go:295 +0x45
runtime.cgocallback(0x0, 0x0, 0x0)
	/usr/local/go/src/runtime/asm_amd64.s:1160 +0xcc

goroutine 15 [running]:
github.com/livekit/sip/pkg/sip/pipeline/elements/sipbin.(*SipTrack).watchContinuity.func1(0x1, 0x2)
	/workspace/pkg/sip/pipeline/elements/sipbin/continuity.go:171 +0x45
runtime.cgocallback(0x0, 0x0, 0x0)
	/usr/local/go/src/runtime/asm_amd64.s:1160 +0xcc

goroutine 16 [chan receive, 30 minutes]:
github.com/livekit/sip/pkg/sip.(*inboundCall).waitForCallEnd(0x1)
	/workspace/pkg/sip/inbound.go:2048 +0x45
`

func TestSummarizeCallbacks(t *testing.T) {
	got := summarizeCallbacks(closeDiagDump)
	require.Equal(t,
		"2x select: github.com/pion/transport/v3/packetio.(*Buffer).Read < livekitbin/livekittracks.(*SrcTrackRtp).Fill < gst/base.goGstBaseSrcFill"+
			" | 1x sync.Mutex.Lock: sipbin.(*SipTrack).settleContinuity < sipbin.(*SipTrack).watchContinuity.func1 < gst.goPadProbeFunc",
		got)
	require.Equal(t, "none", summarizeCallbacks("goroutine 1 [running]:\nmain.main()\n"))
}

func TestStuckElements(t *testing.T) {
	gst.Init(nil)
	pipeline, err := gst.NewPipelineFromString("fakesrc is-live=true name=src ! fakesink sync=false name=sink")
	require.NoError(t, err)
	require.NoError(t, pipeline.SetState(gst.StatePlaying))
	require.Eventually(t, func() bool { return pipeline.GetCurrentState() == gst.StatePlaying }, 5*time.Second, 10*time.Millisecond)

	report := stuckElements(pipeline)
	require.Contains(t, report, "\npipeline=PLAYING:")
	require.Contains(t, report, "fakesrc/src:PLAYING:")
	require.Contains(t, report, "fakesink/sink:PLAYING:")
	require.Contains(t, report, "\nblocked_pads=none")

	require.NoError(t, pipeline.SetState(gst.StateNull))
	report = stuckElements(pipeline)
	require.True(t, strings.HasPrefix(report, "\npipeline=NULL:SUCCESS"), report)
	require.Contains(t, report, "\nnot_null=none")
}
