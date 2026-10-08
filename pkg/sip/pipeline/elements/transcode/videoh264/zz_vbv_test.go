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

package videoh264_test

import (
	"testing"

	"github.com/go-gst/go-gst/gst"
	"github.com/stretchr/testify/require"

	"github.com/livekit/sip/pkg/sip/pipeline/elements/testutils"
)

func TestVideoH264_CameraVBV(t *testing.T) {
	defer testutils.AssertNoLeaks(t)
	elem, err := gst.NewElement("video-h264")
	require.NoError(t, err)
	children, err := gst.ToGstBin(elem).GetElements()
	require.NoError(t, err)

	found := false
	for _, child := range children {
		if factory := child.GetFactory(); factory == nil || factory.GetName() != "x264enc" {
			continue
		}
		vbv, err := child.GetProperty("vbv-buf-capacity")
		require.NoError(t, err)
		require.EqualValues(t, 1000, vbv)
		found = true
	}
	require.True(t, found, "no x264enc in the camera encoder bin")
}
