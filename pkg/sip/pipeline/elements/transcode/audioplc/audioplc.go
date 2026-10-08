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

package audioplc

import (
	"fmt"
	"sync/atomic"
	"time"

	"github.com/go-gst/go-gst/gst"
)

type Stats struct {
	gaps      atomic.Int64
	concealed atomic.Int64
}

func New() (*gst.Element, *Stats, error) {
	plc, err := gst.NewElement("spanplc")
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create spanplc element: %w", err)
	}
	sink := plc.GetStaticPad("sink")
	if sink == nil {
		return nil, nil, fmt.Errorf("spanplc element has no sink pad")
	}
	stats := &Stats{}
	sink.AddProbe(gst.PadProbeTypeEventDownstream, func(_ *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		ev := info.GetEvent()
		if ev == nil || ev.Type() != gst.EventTypeGap {
			return gst.PadProbeOK
		}
		stats.gaps.Add(1)
		if _, duration := ev.ParseGap(); duration > 0 && duration < time.Minute {
			stats.concealed.Add(int64(duration))
		}
		return gst.PadProbeOK
	})
	return plc, stats, nil
}

func (s *Stats) Summary() (int64, time.Duration) {
	if s == nil {
		return 0, 0
	}
	return s.gaps.Load(), time.Duration(s.concealed.Load())
}
