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
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBoundedReturnsAtTimeout(t *testing.T) {
	block := make(chan struct{})
	defer close(block)

	start := time.Now()
	_, err := Bounded("test", 50*time.Millisecond, func() int {
		<-block
		return 1
	})
	require.ErrorIs(t, err, ErrCallTimeout)
	require.Less(t, time.Since(start), time.Second)

	v, err := Bounded("test", time.Second, func() int { return 2 })
	require.NoError(t, err)
	require.Equal(t, 2, v)
}

func TestReadStatsDoesNotStackBehindAStuckRead(t *testing.T) {
	p := &Pipeline{}
	p.stats.Store(&CallStats{TotalRxBytes: 7})
	p.statsBusy.Store(true)

	start := time.Now()
	stats, err := p.readStats()
	require.ErrorIs(t, err, ErrStatsBusy)
	require.Less(t, time.Since(start), 100*time.Millisecond)
	require.NotNil(t, stats)
	require.Equal(t, int64(7), stats.TotalRxBytes)
}
