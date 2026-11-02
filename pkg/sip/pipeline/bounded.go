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
	"errors"
	"fmt"
	"time"

	"github.com/livekit/sip/pkg/sip/pipeline/metrics"
)

var (
	ErrCallTimeout = errors.New("pipeline call timed out")
	ErrStatsBusy   = errors.New("previous stats read still running")
)

var statsTimeout = 2 * time.Second

func Bounded[T any](name string, timeout time.Duration, f func() T) (T, error) {
	done := make(chan T, 1)
	go func() {
		done <- f()
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case v := <-done:
		return v, nil
	case <-timer.C:
		metrics.PipelineCallTimeout(name)
		var zero T
		return zero, fmt.Errorf("%s: %w", name, ErrCallTimeout)
	}
}

type statsResult struct {
	stats *CallStats
	err   error
}

func (p *Pipeline) readStats() (*CallStats, error) {
	if !p.statsBusy.CompareAndSwap(false, true) {
		return p.stats.Load(), ErrStatsBusy
	}
	res, err := Bounded("stats", statsTimeout, func() statsResult {
		defer p.statsBusy.Store(false)
		stats, err := p.getStats()
		return statsResult{stats: stats, err: err}
	})
	if err != nil {
		return p.stats.Load(), err
	}
	return res.stats, res.err
}
