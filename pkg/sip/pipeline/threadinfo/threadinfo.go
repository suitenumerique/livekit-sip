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

// Package threadinfo reads the state of the native threads of the process
// from /proc, by thread name.
package threadinfo

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	taskDir     = "/proc/self/task"
	maxNameLen  = 15
	clockTicksS = 100
)

// Thread is the state of one native thread.
type Thread struct {
	TID   int
	Name  string
	State string
	WChan string
	CPU   time.Duration
}

// Snapshot is the set of threads of the process, by name.
type Snapshot map[string][]Thread

// PadTaskName returns the name GStreamer gives to the streaming thread of a
// pad task.
func PadTaskName(element, pad string) string {
	name := element + ":" + pad
	if len(name) > maxNameLen {
		name = name[:maxNameLen]
	}
	return name
}

// Take reads every thread of the process.
func Take() (Snapshot, error) {
	entries, err := os.ReadDir(taskDir)
	if err != nil {
		return nil, err
	}
	snap := make(Snapshot, len(entries))
	for _, entry := range entries {
		tid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		th, ok := read(tid)
		if !ok {
			continue
		}
		snap[th.Name] = append(snap[th.Name], th)
	}
	return snap, nil
}

func read(tid int) (Thread, bool) {
	base := filepath.Join(taskDir, strconv.Itoa(tid))
	stat, err := os.ReadFile(filepath.Join(base, "stat"))
	if err != nil {
		return Thread{}, false
	}
	th, ok := parseStat(tid, string(stat))
	if !ok {
		return Thread{}, false
	}
	if wchan, err := os.ReadFile(filepath.Join(base, "wchan")); err == nil {
		th.WChan = strings.TrimSpace(string(wchan))
	}
	return th, true
}

// parseStat reads the name, state and CPU time of a /proc/<pid>/task/<tid>/stat line.
func parseStat(tid int, stat string) (Thread, bool) {
	open := strings.IndexByte(stat, '(')
	end := strings.LastIndexByte(stat, ')')
	if open < 0 || end < open {
		return Thread{}, false
	}
	th := Thread{TID: tid, Name: stat[open+1 : end]}
	fields := strings.Fields(stat[end+1:])
	if len(fields) < 13 {
		return th, true
	}
	th.State = fields[0]
	utime, _ := strconv.ParseInt(fields[11], 10, 64)
	stime, _ := strconv.ParseInt(fields[12], 10, 64)
	th.CPU = time.Duration(utime+stime) * time.Second / clockTicksS
	return th, true
}

// Describe returns the state of the threads named name, with the CPU they
// used since before when given.
func (s Snapshot) Describe(name string, before Snapshot) string {
	threads := s[name]
	if len(threads) == 0 {
		return "no thread"
	}
	parts := make([]string, 0, len(threads))
	for _, th := range threads {
		part := th.State
		if th.WChan != "" && th.WChan != "0" {
			part += " " + th.WChan
		}
		if before != nil {
			for _, prev := range before[name] {
				if prev.TID == th.TID && th.CPU > prev.CPU {
					part += fmt.Sprintf(" cpu=%dms", (th.CPU - prev.CPU).Milliseconds())
				}
			}
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, "|")
}
