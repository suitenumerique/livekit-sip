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

package sip

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/livekit/sip/pkg/sip/pipeline"
)

func TestDuplicateDTMF(t *testing.T) {
	t0 := time.Now()
	ev := func(nb, method int, ms int) pipeline.DTMFEvent {
		return pipeline.DTMFEvent{Number: nb, Method: method, At: t0.Add(time.Duration(ms) * time.Millisecond)}
	}
	var accepted []pipeline.DTMFEvent
	var last pipeline.DTMFEvent
	for _, e := range []pipeline.DTMFEvent{
		ev(5, 1, 0), ev(5, 1, 28), ev(5, 1, 58),
		ev(3, 1, 989), ev(3, 1, 989), ev(3, 2, 1040),
		ev(3, 1, 1150),
		ev(7, 1, 1170),
	} {
		if _, dup := duplicateDTMF(last, e); dup {
			continue
		}
		last = e
		accepted = append(accepted, e)
	}
	require.Equal(t, []pipeline.DTMFEvent{ev(5, 1, 0), ev(3, 1, 989), ev(3, 1, 1150), ev(7, 1, 1170)}, accepted)
}
