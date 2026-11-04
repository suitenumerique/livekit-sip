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

package pipeline

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-gst/go-glib/glib"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/sip/pkg/sip/pipeline/metrics"
)

var mainLoopWatchOnce sync.Once

func WatchMainLoop(log logger.Logger, dumpDir string) {
	mainLoopWatchOnce.Do(func() {
		var beats atomic.Uint64
		last := time.Now()
		if _, err := glib.TimeoutAdd(uint(loopBeatPeriod/time.Millisecond), func() bool {
			now := time.Now()
			metrics.MainLoopBeatDelay(max(0, now.Sub(last)-loopBeatPeriod))
			last = now
			beats.Add(1)
			return true
		}); err != nil {
			log.Errorw("Failed to add the GLib main loop heartbeat", err)
			return
		}
		go watchMainLoop(log, dumpDir, &beats)
	})
}

func watchMainLoop(log logger.Logger, dumpDir string, beats *atomic.Uint64) {
	var watch loopWatch
	dumps := 0
	ticker := time.NewTicker(loopBeatPeriod)
	defer ticker.Stop()
	last := time.Now()
	for now := range ticker.C {
		event, silent := watch.tick(now.Sub(last), beats.Load())
		last = now
		switch event {
		case loopEventSlow:
			log.Warnw("GLib main loop was blocked", nil, "blocked", silent)
		case loopEventStalled:
			metrics.MainLoopStalled(true)
			dump := ""
			if dumps < loopMaxDumps {
				dumps++
				path, err := writeGoroutineDump(dumpDir, now)
				if err != nil {
					log.Warnw("Failed to dump the goroutines of a stalled GLib main loop", err)
				}
				dump = path
			}
			log.Errorw("GLib main loop stalled", nil, "silent", silent, "dump", dump)
		case loopEventRecovered:
			metrics.MainLoopStalled(false)
			log.Warnw("GLib main loop recovered", nil, "blocked", silent)
		}
	}
}
