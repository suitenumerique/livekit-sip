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

package livekitcompositor

import (
	"fmt"
	"io/fs"
	"sync"

	"github.com/livekit/sip/res"
	"github.com/vopenia-io/go-pangocairo/pango"
)

var fontsOnce sync.Once

// registerFonts adds the embedded fonts to fontconfig. The memfds stay open
// for the life of the process: FreeType reopens the path each time it loads a face.
func registerFonts() {
	fontsOnce.Do(func() {
		entries, err := fs.ReadDir(res.Fonts, "fonts")
		if err != nil {
			return
		}
		for _, entry := range entries {
			data, err := res.Fonts.ReadFile("fonts/" + entry.Name())
			if err != nil {
				continue
			}
			fd, err := res.MemfdFromBytes(entry.Name(), data)
			if err != nil {
				continue
			}
			pango.AddFont(fmt.Sprintf("/proc/self/fd/%d", fd))
		}
	})
}
