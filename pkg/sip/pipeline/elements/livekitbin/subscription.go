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

import (
	"fmt"

	"github.com/go-gst/go-gst/gst"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

// The SDK does not report a subscription the bin gives up itself: after a
// local SetSubscribed(false) the publication keeps its track, IsSubscribed()
// stays true and OnTrackUnsubscribed never fires. The bin keeps its own record
// of the tracks it asked the SFU for and removes the receive chain itself when
// it releases one.

// wantTrack records a subscription request for sid. It returns false when the
// request is already recorded, so a track still on its way is not requested
// twice.
func (e *LivekitBin) wantTrack(sid string) bool {
	e.subMu.Lock()
	defer e.subMu.Unlock()
	if e.wanted == nil {
		e.wanted = make(map[string]struct{})
	}
	if _, ok := e.wanted[sid]; ok {
		return false
	}
	e.wanted[sid] = struct{}{}
	return true
}

// dropTrack forgets the subscription request for sid and reports whether one
// was recorded.
func (e *LivekitBin) dropTrack(sid string) bool {
	e.subMu.Lock()
	defer e.subMu.Unlock()
	_, ok := e.wanted[sid]
	delete(e.wanted, sid)
	return ok
}

// wantsTrack reports whether sid was requested from the SFU and not released.
func (e *LivekitBin) wantsTrack(sid string) bool {
	e.subMu.Lock()
	defer e.subMu.Unlock()
	_, ok := e.wanted[sid]
	return ok
}

// subscribeTrack asks the SFU for pub unless the request is already pending.
func (e *LivekitBin) subscribeTrack(self *gst.Bin, pub *lksdk.RemoteTrackPublication, reason string) error {
	sid := pub.SID()
	if !e.wantTrack(sid) {
		return nil
	}
	if err := pub.SetSubscribed(true); err != nil {
		e.dropTrack(sid)
		return err
	}
	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Subscribed to track\nsid=%s\nsource=%s\nreason=%s", sid, pub.Source(), reason))
	return nil
}

// releaseTrack gives pub up: the receive chain is removed here, then the SFU
// is told to stop sending. It must not run under e.mu.
func (e *LivekitBin) releaseTrack(self *gst.Bin, pub *lksdk.RemoteTrackPublication, reason string) error {
	sid := pub.SID()
	if !e.dropTrack(sid) {
		return nil
	}
	if t, ok := e.lookupTrack(sid); ok {
		e.UnsubscribeTrack(t.Track, t.Pub, t.Rp)
	}
	if err := pub.SetSubscribed(false); err != nil {
		return err
	}
	self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Released track\nsid=%s\nsource=%s\nreason=%s", sid, pub.Source(), reason))
	return nil
}
