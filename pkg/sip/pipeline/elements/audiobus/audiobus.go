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

// Package audiobus holds the raw audio format shared by the SIP and LiveKit audio paths.
package audiobus

const Rate = 48000

const Caps = "audio/x-raw,format=S16LE,rate=48000,channels=1,layout=interleaved"

const MixCaps = "audio/x-raw,format=F32LE,rate=48000,channels=1,layout=interleaved"
