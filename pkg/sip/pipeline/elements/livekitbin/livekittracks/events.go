package livekittracks

import (
	"fmt"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/samber/lo"
)

const (
	EventTrackSourceInfo     = "livekitbin_srctrack-event-info"
	EventActiveSpeakerChange = "livekitbin_activespeaker-change"
)

type TrackSourceInfo struct {
	ParticipantSID  string
	ParticipantName string
	TrackSID        string
	Source          livekit.TrackSource
	Kind            lksdk.TrackKind
	MimeType        string
	SSRC            uint
	PT              uint
}

func NewTrackSourceInfo(participant *lksdk.RemoteParticipant, publication *lksdk.RemoteTrackPublication) TrackSourceInfo {
	track := publication.TrackRemote()
	return TrackSourceInfo{
		ParticipantSID:  participant.SID(),
		ParticipantName: participant.Identity(),
		TrackSID:        publication.SID(),
		Source:          publication.Source(),
		Kind:            publication.Kind(),
		MimeType:        track.Codec().MimeType,
		SSRC:            uint(track.SSRC()),
		PT:              uint(track.PayloadType()),
	}
}

func (t TrackSourceInfo) Structure() *gst.Structure {
	s := gst.NewStructure(EventTrackSourceInfo)
	lo.Must0(s.SetValue("participant-sid", t.ParticipantSID))
	lo.Must0(s.SetValue("participant-name", t.ParticipantName))
	lo.Must0(s.SetValue("track-sid", t.TrackSID))
	lo.Must0(s.SetValue("source", int(t.Source)))
	lo.Must0(s.SetValue("kind", string(t.Kind)))
	lo.Must0(s.SetValue("mime-type", t.MimeType))
	lo.Must0(s.SetValue("ssrc", t.SSRC))
	lo.Must0(s.SetValue("pt", t.PT))
	return s
}

func TrackSourceInfoFromStructure(s *gst.Structure) (TrackSourceInfo, error) {
	if s.Name() != EventTrackSourceInfo {
		return TrackSourceInfo{}, fmt.Errorf("invalid structure name: expected %s, got %s", EventTrackSourceInfo, s.Name())
	}

	sourceInfo := TrackSourceInfo{}
	participantSIDVal, err := s.GetValue("participant-sid")
	if err != nil {
		return TrackSourceInfo{}, err
	}
	participantSID, ok := participantSIDVal.(string)
	if !ok {
		return TrackSourceInfo{}, fmt.Errorf("invalid participant-sid value: expected string, got %T", participantSIDVal)
	}
	sourceInfo.ParticipantSID = participantSID
	participantNameVal, err := s.GetValue("participant-name")
	if err != nil {
		return TrackSourceInfo{}, err
	}
	participantName, ok := participantNameVal.(string)
	if !ok {
		return TrackSourceInfo{}, fmt.Errorf("invalid participant-name value: expected string, got %T", participantNameVal)
	}
	sourceInfo.ParticipantName = participantName
	trackSIDVal, err := s.GetValue("track-sid")
	if err != nil {
		return TrackSourceInfo{}, err
	}
	trackSID, ok := trackSIDVal.(string)
	if !ok {
		return TrackSourceInfo{}, fmt.Errorf("invalid track-sid value: expected string, got %T", trackSIDVal)
	}
	sourceInfo.TrackSID = trackSID
	sourceVal, err := s.GetValue("source")
	if err != nil {
		return TrackSourceInfo{}, err
	}
	sourceInt, ok := sourceVal.(int)
	if !ok {
		return TrackSourceInfo{}, fmt.Errorf("invalid source value: expected int, got %T", sourceVal)
	}
	sourceInfo.Source = livekit.TrackSource(sourceInt)
	kindVal, err := s.GetValue("kind")
	if err != nil {
		return TrackSourceInfo{}, err
	}
	kindStr, ok := kindVal.(string)
	if !ok {
		return TrackSourceInfo{}, fmt.Errorf("invalid kind value: expected string, got %T", kindVal)
	}
	sourceInfo.Kind = lksdk.TrackKind(kindStr)
	mimeTypeVal, err := s.GetValue("mime-type")
	if err != nil {
		return TrackSourceInfo{}, err
	}
	mimeType, ok := mimeTypeVal.(string)
	if !ok {
		return TrackSourceInfo{}, fmt.Errorf("invalid mime-type value: expected string, got %T", mimeTypeVal)
	}
	sourceInfo.MimeType = mimeType
	ssrcVal, err := s.GetValue("ssrc")
	if err != nil {
		return TrackSourceInfo{}, err
	}
	ssrc, ok := ssrcVal.(uint)
	if !ok {
		return TrackSourceInfo{}, fmt.Errorf("invalid ssrc value: expected uint, got %T", ssrcVal)
	}
	sourceInfo.SSRC = ssrc
	ptVal, err := s.GetValue("pt")
	if err != nil {
		return TrackSourceInfo{}, err
	}
	pt, ok := ptVal.(uint)
	if !ok {
		return TrackSourceInfo{}, fmt.Errorf("invalid pt value: expected uint, got %T", ptVal)
	}
	sourceInfo.PT = pt

	return sourceInfo, nil
}

func PadGetTrackSourceInfo(pad *gst.Pad) (TrackSourceInfo, error) {
	var event *gst.Event
	for i := 0; ; i++ {
		evt := pad.GetStickyEvent(gst.EventTypeCustomDownstreamSticky, uint(i))
		if evt == nil {
			break
		}
		if evt.HasName(EventTrackSourceInfo) {
			event = evt
			break
		}
	}
	if event == nil {
		return TrackSourceInfo{}, fmt.Errorf("no event found")
	}
	return TrackSourceInfoFromStructure(event.GetStructure())
}

func PadOnTrackSourceInfo(pad *gst.Pad, callback func(pad *gst.Pad, info TrackSourceInfo)) {
	pad.AddProbe(gst.PadProbeTypeEventDownstream, func(pad *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		if info.GetEvent() != nil && info.GetEvent().HasName(EventTrackSourceInfo) {
			sourceInfo, err := TrackSourceInfoFromStructure(info.GetEvent().GetStructure())
			if err != nil {
				parent := pad.GetParentElement()
				if parent != nil {
					parent.Log(CAT, gst.LevelError, fmt.Sprintf("failed to parse track source info event: %v", err))
				} else {
					CAT.Log(gst.LevelError, fmt.Sprintf("failed to parse track source info event: %v", err))
				}
				return gst.PadProbeOK
			}
			callback(pad, sourceInfo)
		}
		return gst.PadProbeOK
	})
	info, err := PadGetTrackSourceInfo(pad)
	if err != nil {
		if err.Error() == "no event found" {
			return
		}
		parent := pad.GetParentElement()
		if parent != nil {
			parent.Log(CAT, gst.LevelError, fmt.Sprintf("failed to get initial track source info: %v", err))
		} else {
			CAT.Log(gst.LevelError, fmt.Sprintf("failed to get initial track source info: %v", err))
		}
		return
	}
	callback(pad, info)
}

type ActiveSpeakerChangeInfo struct {
	ParticipantsSID   []string
	AudioLevels       map[string]float64
	ParticipantTracks map[string][]string
}

func NewActiveSpeakerChangeInfo(activeParticipants []lksdk.Participant) ActiveSpeakerChangeInfo {
	participantSIDs := make([]string, len(activeParticipants))
	audioLevels := make(map[string]float64)
	participantTracks := make(map[string][]string)
	for i, participant := range activeParticipants {
		participantSIDs[i] = participant.SID()
		rp, ok := participant.(*lksdk.RemoteParticipant)
		if !ok {
			// can be local participant when we are speaking
			continue
		}
		pub := rp.TrackPublications()
		trackSIDs := make([]string, len(pub))
		for j, track := range pub {
			trackSIDs[j] = track.SID()
		}
		participantTracks[participant.SID()] = trackSIDs
		audioLevels[participant.SID()] = float64(participant.AudioLevel())
	}
	return ActiveSpeakerChangeInfo{
		ParticipantsSID:   participantSIDs,
		AudioLevels:       audioLevels,
		ParticipantTracks: participantTracks,
	}
}

func newArrayT[T any](items []T) (*glib.Array, error) {
	return glib.NewArray(lo.Map(items, func(item T, _ int) interface{} { return item }))
}

func (a ActiveSpeakerChangeInfo) Structure() *gst.Structure {
	s := gst.NewStructure(EventActiveSpeakerChange)
	lo.Must0(s.SetValue("participants-sid", lo.Must(newArrayT(a.ParticipantsSID))))
	audioLevels := gst.NewStructure("audio-levels")
	for participantSID, audioLevel := range a.AudioLevels {
		lo.Must0(audioLevels.SetDouble(participantSID, audioLevel))
	}
	tracks := gst.NewStructure("participant-tracks")
	for participantSID, trackSIDs := range a.ParticipantTracks {
		lo.Must0(tracks.SetValue(participantSID, lo.Must(newArrayT(trackSIDs))))
	}
	lo.Must0(s.SetValue("audio-levels", audioLevels))
	lo.Must0(s.SetValue("participant-tracks", tracks))
	return s
}

func ActiveSpeakerChangeInfoFromStructure(s *gst.Structure) (ActiveSpeakerChangeInfo, error) {
	if s.Name() != EventActiveSpeakerChange {
		return ActiveSpeakerChangeInfo{}, fmt.Errorf("invalid structure name: expected %s, got %s", EventActiveSpeakerChange, s.Name())
	}

	participantsSIDVal, err := s.GetValue("participants-sid")
	if err != nil {
		return ActiveSpeakerChangeInfo{}, err
	}
	participantsSIDArr, ok := participantsSIDVal.(*glib.Array)
	if !ok {
		return ActiveSpeakerChangeInfo{}, fmt.Errorf("invalid participants-sid value: expected array, got %T", participantsSIDVal)
	}
	participantsSIDVals, err := participantsSIDArr.Values()
	if err != nil {
		return ActiveSpeakerChangeInfo{}, err
	}
	participantsSIDs := make([]string, len(participantsSIDVals))
	for i, val := range participantsSIDVals {
		sid, ok := val.(string)
		if !ok {
			return ActiveSpeakerChangeInfo{}, fmt.Errorf("invalid participants-sid array value: expected string, got %T", val)
		}
		participantsSIDs[i] = sid
	}

	audioLevelsVal, err := s.GetValue("audio-levels")
	if err != nil {
		return ActiveSpeakerChangeInfo{}, err
	}
	audioLevelsStruct, ok := audioLevelsVal.(*gst.Structure)
	if !ok {
		return ActiveSpeakerChangeInfo{}, fmt.Errorf("invalid audio-levels value: expected structure, got %T", audioLevelsVal)
	}
	audioLevels := make(map[string]float64)
	for k, val := range audioLevelsStruct.Values() {
		level, ok := val.(float64)
		if !ok {
			return ActiveSpeakerChangeInfo{}, fmt.Errorf("invalid audio-levels structure value: expected float64, got %T", val)
		}
		audioLevels[k] = level
	}

	participantTracksVal, err := s.GetValue("participant-tracks")
	if err != nil {
		return ActiveSpeakerChangeInfo{}, err
	}
	participantTracksStruct, ok := participantTracksVal.(*gst.Structure)
	if !ok {
		return ActiveSpeakerChangeInfo{}, fmt.Errorf("invalid participant-tracks value: expected structure, got %T", participantTracksVal)
	}
	participantTracks := make(map[string][]string)
	for k, val := range participantTracksStruct.Values() {
		sidArr, ok := val.(*glib.Array)
		if !ok {
			return ActiveSpeakerChangeInfo{}, fmt.Errorf("invalid participant-tracks structure value: expected array, got %T", val)
		}
		sidVals, err := sidArr.Values()
		if err != nil {
			return ActiveSpeakerChangeInfo{}, err
		}
		sids := make([]string, len(sidVals))
		for i, sidVal := range sidVals {
			sid, ok := sidVal.(string)
			if !ok {
				return ActiveSpeakerChangeInfo{}, fmt.Errorf("invalid participant-tracks structure array value: expected string, got %T", sidVal)
			}
			sids[i] = sid
		}
		participantTracks[k] = sids
	}

	return ActiveSpeakerChangeInfo{
		ParticipantsSID:   participantsSIDs,
		AudioLevels:       audioLevels,
		ParticipantTracks: participantTracks,
	}, nil
}

type ParticipantInfo struct {
	SID      string
	Name     string
	Identity string
	Level    float64
}

func NewParticipantInfo(participant lksdk.Participant) ParticipantInfo {
	return ParticipantInfo{
		SID:      participant.SID(),
		Name:     participant.Name(),
		Identity: participant.Identity(),
		Level:    float64(participant.AudioLevel()),
	}
}

func (p ParticipantInfo) Structure() *gst.Structure {
	s := gst.NewStructure("livekitbin_participant_info")
	lo.Must0(s.SetValue("sid", p.SID))
	lo.Must0(s.SetValue("name", p.Name))
	lo.Must0(s.SetValue("identity", p.Identity))
	lo.Must0(s.SetValue("level", p.Level))
	return s
}

func ParticipantInfoFromStructure(s *gst.Structure) (ParticipantInfo, error) {
	if s.Name() != "livekitbin_participant_info" {
		return ParticipantInfo{}, fmt.Errorf("invalid structure name: expected livekitbin_participant_info, got %s", s.Name())
	}
	sidVal, err := s.GetString("sid")
	if err != nil {
		return ParticipantInfo{}, fmt.Errorf("invalid sid value: %v", err)
	}
	nameVal, err := s.GetString("name")
	if err != nil {
		return ParticipantInfo{}, fmt.Errorf("invalid name value: %v", err)
	}
	identityVal, err := s.GetString("identity")
	if err != nil {
		return ParticipantInfo{}, fmt.Errorf("invalid identity value: %v", err)
	}
	levelVal, err := s.GetDouble("level")
	if err != nil {
		return ParticipantInfo{}, fmt.Errorf("invalid level value: %v", err)
	}
	return ParticipantInfo{
		SID:      sidVal,
		Name:     nameVal,
		Identity: identityVal,
		Level:    levelVal,
	}, nil
}
