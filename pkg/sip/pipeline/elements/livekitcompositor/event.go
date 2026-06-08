package livekitcompositor

import (
	"fmt"

	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/livekitbin/livekittracks"
	"github.com/samber/lo"
)

func (e *LivekitCompositor) onActiveSpeakersChanged(instance *gst.Element, structure *gst.Structure) {
	self := gst.ToGstBin(instance)

	info, err := livekittracks.ActiveSpeakerChangeInfoFromStructure(structure)
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to parse active speakers change info: %v", err))
		self.Error("Failed to parse active speakers change info", err)
		return
	}

	self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Active speakers changed: %v", info))

	e.mu.Lock()
	defer e.mu.Unlock()

	for sid, p := range e.participants {
		p.Level = 0
		e.participants[sid] = p
	}

	info.ParticipantsSID = lo.Filter(info.ParticipantsSID, func(sid string, _ int) bool {
		if _, exist := e.participants[sid]; !exist {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Participant SID from active speaker change info not found in participants map: %s", sid))
			return false
		}
		return true
	})

	for sid, l := range info.AudioLevels {
		p, ok := e.participants[sid]
		if ok {
			p.Level = l
			e.participants[sid] = p
		}
	}

	layout := make([]string, len(info.ParticipantsSID))

	newParticipants := make([]string, 0, len(info.ParticipantsSID))
	for _, sid := range info.ParticipantsSID {
		idx := lo.IndexOf(e.currentLayout, sid)
		if idx == -1 || idx >= len(layout) {
			newParticipants = append(newParticipants, sid)
			continue
		}
		layout[idx] = sid
	}

	for i := range len(layout) {
		if layout[i] != "" {
			continue
		}
		if len(newParticipants) == 0 {
			break
		}
		layout[i] = newParticipants[0]
		newParticipants = newParticipants[1:]
	}

	e.applyMicrophoneLayout(self, layout)
	e.applyCameraLayout(self, layout)

	e.currentLayout = layout
}

func (e *LivekitCompositor) onParticipantJoin(instance *gst.Element, structure *gst.Structure) {
	self := gst.ToGstBin(instance)

	info, err := livekittracks.ParticipantInfoFromStructure(structure)
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to parse participant info from structure: %v", err))
		self.Error("Failed to parse participant info from structure", err)
		return
	}

	self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Participant joined: %s", info.SID))

	e.mu.Lock()
	defer e.mu.Unlock()

	e.participants[info.SID] = info
}

func (e *LivekitCompositor) onParticipantLeft(instance *gst.Element, structure *gst.Structure) {
	self := gst.ToGstBin(instance)

	info, err := livekittracks.ParticipantInfoFromStructure(structure)
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to parse participant info from structure: %v", err))
		self.Error("Failed to parse participant info from structure", err)
		return
	}

	self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Participant left: %s", info.SID))
	e.mu.Lock()
	defer e.mu.Unlock()

	delete(e.participants, info.SID)
}
