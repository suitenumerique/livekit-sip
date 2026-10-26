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

package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParkedIdleRestart(t *testing.T) {
	t0 := time.Now()
	at := func(d time.Duration) time.Time { return t0.Add(d) }

	idle, restart := parkedIdleRestart(0, 0, time.Time{}, at(0))
	require.True(t, idle.IsZero())
	require.False(t, restart)

	idle, restart = parkedIdleRestart(1, 2, time.Time{}, at(0))
	require.True(t, idle.IsZero())
	require.False(t, restart)

	idle, restart = parkedIdleRestart(1, 0, time.Time{}, at(0))
	require.Equal(t, at(0), idle)
	require.False(t, restart)

	idle, restart = parkedIdleRestart(1, 0, idle, at(4*time.Minute))
	require.Equal(t, at(0), idle)
	require.False(t, restart)

	reset, restart := parkedIdleRestart(1, 1, idle, at(4*time.Minute+30*time.Second))
	require.True(t, reset.IsZero())
	require.False(t, restart)

	_, restart = parkedIdleRestart(1, 0, idle, at(5*time.Minute))
	require.True(t, restart)
}
