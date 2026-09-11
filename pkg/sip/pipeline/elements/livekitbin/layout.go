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

package livekitbin

// nextLayout computes the mosaic membership, capped at max: the current
// speakers first, then the members of the previous layout that are still
// present, then the remaining participants in the (stable) order given.
// others is the set of present participants; speakers or previous members
// missing from it are ignored. Membership therefore only changes when a
// newcomer speaks, a slot is free or a member leaves, never with map order.
func nextLayout(prev, speakers, others []string, max int) []string {
	if max <= 0 {
		return nil
	}
	present := make(map[string]struct{}, len(others))
	for _, sid := range others {
		present[sid] = struct{}{}
	}

	out := make([]string, 0, max)
	seen := make(map[string]struct{}, max)
	add := func(sid string) {
		if len(out) >= max {
			return
		}
		if _, ok := seen[sid]; ok {
			return
		}
		if _, ok := present[sid]; !ok {
			return
		}
		seen[sid] = struct{}{}
		out = append(out, sid)
	}
	for _, sid := range speakers {
		add(sid)
	}
	for _, sid := range prev {
		add(sid)
	}
	for _, sid := range others {
		add(sid)
	}
	return out
}
