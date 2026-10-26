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
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-gst/go-gst/gst"
)

const (
	recvWalkDepth       = 16
	recvSwitchLogDelay  = 10 * time.Second
	inputSelectorFamily = "input-selector"
)

// RecvBranch describes the payload type branch of a received RTP stream.
type RecvBranch struct {
	PT       uint
	Entry    string
	Path     []string
	Selector string
	Active   bool
}

// RecvBranches describes every payload type branch of the stream received
// on session with ssrc.
func (sio *SipIo) RecvBranches(session uint, ssrc uint32) []RecvBranch {
	prefix := fmt.Sprintf("%d_%d_", session, ssrc)
	type entry struct {
		pt  uint
		hop *Hop
	}
	var entries []entry
	sio.hopMu.Lock()
	for key, h := range sio.hops {
		if h == nil || !strings.HasPrefix(key, prefix) {
			continue
		}
		pt, err := strconv.ParseUint(strings.TrimPrefix(key, prefix), 10, 32)
		if err != nil {
			continue
		}
		entries = append(entries, entry{pt: uint(pt), hop: h})
	}
	sio.hopMu.Unlock()
	slices.SortFunc(entries, func(a, b entry) int { return int(a.pt) - int(b.pt) })

	out := make([]RecvBranch, 0, len(entries))
	for _, en := range entries {
		b := RecvBranch{PT: en.pt}
		if en.hop.srcPad != nil {
			b.Entry = en.hop.srcPad.GetLastFlowReturn().String()
		}
		if en.hop.src != nil {
			if pad := en.hop.src.GetStaticPad("src"); pad != nil {
				b.Path, b.Selector, b.Active = walkDownstream(pad, recvWalkDepth)
			}
		}
		out = append(out, b)
	}
	return out
}

// RecvBranches describes every payload type branch of the stream received
// on session with ssrc.
func (p *Pipeline) RecvBranches(session uint, ssrc uint32) []RecvBranch {
	if p == nil || p.SipIo == nil {
		return nil
	}
	return p.SipIo.RecvBranches(session, ssrc)
}

// walkDownstream follows the data flow from start through single-output
// elements and bins, recording the last flow return of each source pad, until
// it reaches an input-selector or a fork.
func walkDownstream(start *gst.Pad, depth int) ([]string, string, bool) {
	var path []string
	pad := start
	for range depth {
		path = append(path, padLabel(pad)+"="+pad.GetLastFlowReturn().String())
		peer := pad.GetPeer()
		if peer == nil {
			return append(path, "unlinked"), "", false
		}
		for peer.IsGhostPad() {
			target := peer.AsGhostPad().GetTarget()
			if target == nil {
				break
			}
			peer = target
		}
		elem := peer.GetParentElement()
		if elem == nil {
			ghost := peer.GetParent()
			if ghost == nil || !ghost.IsA(gst.TypePad) {
				return path, "", false
			}
			pad = gst.ToPad(ghost)
			continue
		}
		if factory := elem.GetFactory(); factory != nil && factory.GetName() == inputSelectorFamily {
			path = append(path, elem.GetName()+"."+peer.GetName())
			active := selectorActivePad(elem)
			return path, active, active != "" && active == peer.GetName()
		}
		srcs, err := elem.GetSrcPads()
		if err != nil || len(srcs) != 1 {
			return append(path, fmt.Sprintf("%s(%d src)", elem.GetName(), len(srcs))), "", false
		}
		pad = srcs[0]
	}
	return path, "", false
}

func padLabel(pad *gst.Pad) string {
	if elem := pad.GetParentElement(); elem != nil {
		return elem.GetName() + "." + pad.GetName()
	}
	return pad.GetName()
}

func selectorActivePad(selector *gst.Element) string {
	v, err := selector.GetProperty("active-pad")
	if err != nil {
		return ""
	}
	pad := gst.ToPad(v)
	if pad == nil || pad.Instance() == nil {
		return ""
	}
	return pad.GetName()
}

// logRecvBranchesLater logs the branches of the stream received on session
// with ssrc once the payload type switch has settled.
func (sio *SipIo) logRecvBranchesLater(session uint, ssrc uint32, pt uint) {
	time.AfterFunc(recvSwitchLogDelay, func() {
		if sio.pipeline == nil || sio.pipeline.Closed() {
			return
		}
		sio.log.Infow("Receive branches after payload type switch", "session", session, "ssrc", ssrc, "newPt", pt, "branches", sio.RecvBranches(session, ssrc))
	})
}
