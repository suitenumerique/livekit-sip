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

import "sync/atomic"

type coalescedRun struct {
	running atomic.Bool
	pending atomic.Bool
}

func (c *coalescedRun) trigger(f func()) {
	c.pending.Store(true)
	if c.running.CompareAndSwap(false, true) {
		go c.run(f)
	}
}

func (c *coalescedRun) run(f func()) {
	for {
		for c.pending.Swap(false) {
			f()
		}
		c.running.Store(false)
		if !c.pending.Load() || !c.running.CompareAndSwap(false, true) {
			return
		}
	}
}
