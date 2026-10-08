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

package livekittracks

import (
	"os"
	"testing"

	"github.com/go-gst/go-gst/gst"
	"github.com/pion/rtcp"
	"github.com/stretchr/testify/require"

	"github.com/livekit/sip/pkg/sip/pipeline/elements/testutils"
)

func TestMain(m *testing.M) {
	gst.Init(nil)
	os.Exit(m.Run())
}

func TestSrcTrackOnRtcp_ReleasedTrack(t *testing.T) {
	defer testutils.AssertNoLeaks(t)
	bin := gst.NewBin("srctrack")
	pad := gst.NewPad("src_rtcp", gst.PadDirectionSource)
	cb := (&SrcTrack{}).onRtcp(bin, pad)

	require.NotPanics(t, func() {
		cb(&rtcp.PictureLossIndication{SenderSSRC: 1, MediaSSRC: 2})
		cb(&rtcp.Goodbye{Sources: []uint32{1}})
		cb(&rtcp.SenderReport{SSRC: 1})
	})
}
