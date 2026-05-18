package livekitbin

import (
	"fmt"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
)

var properties = []*glib.ParamSpec{
	glib.NewStringParam(
		"sid",
		"SID",
		"The room sid",
		nil,
		glib.ParameterReadable,
	),
	glib.NewStringParam(
		"room-name",
		"Room Name",
		"The room name",
		nil,
		glib.ParameterReadable,
	),

	glib.NewStringParam(
		"ws-url",
		"WebSocket URL",
		"LiveKit WebSocket URL",
		nil,
		glib.ParameterReadable|glib.ParameterWritable,
	),
	glib.NewStringParam(
		"token",
		"Token",
		"LiveKit access token",
		nil,
		glib.ParameterWritable,
	),
	glib.NewStringParam(
		"participant-identity",
		"Participant Identity",
		"LiveKit participant identity",
		nil,
		glib.ParameterReadable|glib.ParameterWritable,
	),
	glib.NewStringParam(
		"participant-name",
		"Participant Name",
		"LiveKit participant name",
		nil,
		glib.ParameterReadable|glib.ParameterWritable,
	),
	glib.NewBoxedParam(
		"participant-attributes",
		"Participant Attributes",
		"Attributes of the local participant",
		gst.TypeStructure,
		glib.ParameterReadable|glib.ParameterWritable,
	),
	glib.NewUintParam(
		"max-active-participants",
		"Max Active Participants",
		"Maximum number of active participants. If more participants are in the room, participant tracks will be enabled/disabled based on active speaker detection. Must be greater than 0",
		1,
		uint(MAX_ACTIVE_PARTICIPANTS),
		6,
		glib.ParameterReadable|glib.ParameterWritable,
	),
	glib.NewBoolParam(
		"microphone",
		"Microphone",
		"Whether to subscribe to microphone tracks",
		false,
		glib.ParameterReadable|glib.ParameterWritable,
	),
	glib.NewStringParam(
		"microphone-mime-type",
		"Microphone MIME Type",
		"The MIME type for publishing microphone tracks",
		nil,
		glib.ParameterReadable|glib.ParameterWritable|glib.ParameterConstructOnly,
	),
	glib.NewBoolParam(
		"camera",
		"Camera",
		"Whether to subscribe to camera tracks",
		false,
		glib.ParameterReadable|glib.ParameterWritable,
	),
	glib.NewStringParam(
		"camera-mime-type",
		"Camera MIME Type",
		"The MIME type for publishing camera tracks",
		nil,
		glib.ParameterReadable|glib.ParameterWritable|glib.ParameterConstructOnly,
	),
	glib.NewBoolParam(
		"screenshare",
		"Screen Share",
		"Whether to subscribe to screenshare tracks",
		false,
		glib.ParameterReadable|glib.ParameterWritable,
	),
	glib.NewStringParam(
		"screenshare-mime-type",
		"Screen Share MIME Type",
		"The MIME type for publishing screenshare tracks",
		nil,
		glib.ParameterReadable|glib.ParameterWritable|glib.ParameterConstructOnly,
	),
	glib.NewBoolParam(
		"screenshare-audio",
		"Screen Share Audio",
		"Whether to subscribe to screenshare audio tracks",
		false,
		glib.ParameterReadable|glib.ParameterWritable,
	),
	glib.NewStringParam(
		"screenshare-audio-mime-type",
		"Screen Share Audio MIME Type",
		"The MIME type for publishing screenshare audio tracks",
		nil,
		glib.ParameterReadable|glib.ParameterWritable|glib.ParameterConstructOnly,
	),
}

func stringPropSetter(dst *string) func(self *gst.Bin, param *glib.ParamSpec, value *glib.Value) {
	return func(self *gst.Bin, param *glib.ParamSpec, value *glib.Value) {
		gv, err := value.GoValue()
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error getting %s property value: %v", param.Name(), err))
			return
		}
		val, ok := gv.(string)
		if !ok {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Invalid type for %s property", param.Name()))
			return
		}
		if val != "" {
			*dst = val
		}
	}
}

func stringPropGetter(src *string) func(self *gst.Bin, param *glib.ParamSpec) *glib.Value {
	return func(self *gst.Bin, param *glib.ParamSpec) *glib.Value {
		value, err := glib.GValue(*src)
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error getting %s property value: %v", param.Name(), err))
			return nil
		}
		return value
	}
}

func boolPropSetter(dst *bool) func(self *gst.Bin, param *glib.ParamSpec, value *glib.Value) {
	return func(self *gst.Bin, param *glib.ParamSpec, value *glib.Value) {
		gv, err := value.GoValue()
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error getting %s property value: %v", param.Name(), err))
			return
		}
		val, ok := gv.(bool)
		if !ok {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Invalid type for %s property", param.Name()))
			return
		}
		*dst = val
	}
}

func boolPropGetter(src *bool) func(self *gst.Bin, param *glib.ParamSpec) *glib.Value {
	return func(self *gst.Bin, param *glib.ParamSpec) *glib.Value {
		value, err := glib.GValue(*src)
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error getting %s property value: %v", param.Name(), err))
			return nil
		}
		return value
	}
}

func (e *LivekitBin) SetProperty(instance *glib.Object, id uint, value *glib.Value) {
	self := gst.ToGstBin(instance)
	param := properties[id]
	switch param.Name() {
	case "ws-url":
		if e.Is(RoomStateJoined | RoomStateJoining) {
			self.Log(CAT, gst.LevelWarning, "Changing ws-url while joining or after joining a room is not supported")
			return
		}
		stringPropSetter(&e.wsURL)(self, param, value)
	case "token":
		if e.Is(RoomStateJoined | RoomStateJoining) {
			self.Log(CAT, gst.LevelWarning, "Changing token while joining or after joining a room is not supported")
			return
		}
		stringPropSetter(&e.token)(self, param, value)
	case "participant-identity":
		if e.room.LocalParticipant != nil {
			self.Log(CAT, gst.LevelWarning, "Changing participant identity after joining a room is not supported")
			return
		}
		stringPropSetter(&e.defaultParticipantIdentity)(self, param, value)
	case "participant-name":
		stringPropSetter(&e.defaultParticipantName)(self, param, value)
	case "participant-attributes":
		gv, err := value.GoValue()
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error getting participant-attributes property value: %v", err))
			return
		}
		val, ok := gv.(*gst.Structure)
		if !ok {
			self.Log(CAT, gst.LevelError, "Invalid type for participant-attributes property")
			return
		}
		participantAttributes := make(map[string]string)
		for k, v := range val.Values() {
			str, ok := v.(string)
			if !ok {
				self.Log(CAT, gst.LevelError, fmt.Sprintf("Invalid type for participant attribute %s", k))
				continue
			}
			participantAttributes[k] = str
		}
		if e.room.LocalParticipant == nil {
			e.defaultParticipantAttributes = participantAttributes
		} else {
			e.room.LocalParticipant.SetAttributes(participantAttributes)
		}
	case "max-active-participants":
		gv, err := value.GoValue()
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error getting max-active-participants property value: %v", err))
			return
		}
		val, ok := gv.(uint)
		if !ok {
			self.Log(CAT, gst.LevelError, "Invalid type for max-active-participants property")
			return
		}
		e.maxActiveParticipants = val
	case "microphone":
		old := e.microphone
		boolPropSetter(&e.microphone)(self, param, value)
		if old != e.microphone {
			e.updateSubscriptions(self)
		}
	case "microphone-mime-type":
		stringPropSetter(&e.microphoneMimeType)(self, param, value)
	case "camera":
		old := e.camera
		boolPropSetter(&e.camera)(self, param, value)
		if old != e.camera {
			e.updateSubscriptions(self)
		}
	case "camera-mime-type":
		stringPropSetter(&e.cameraMimeType)(self, param, value)
	case "screenshare":
		old := e.screenshare
		boolPropSetter(&e.screenshare)(self, param, value)
		if old != e.screenshare {
			e.updateSubscriptions(self)
		}
	case "screenshare-mime-type":
		stringPropSetter(&e.screenshareMimeType)(self, param, value)
	case "screenshare-audio":
		old := e.screenshareAudio
		boolPropSetter(&e.screenshareAudio)(self, param, value)
		if old != e.screenshareAudio {
			e.updateSubscriptions(self)
		}
	case "screenshare-audio-mime-type":
		stringPropSetter(&e.screenshareAudioMimeType)(self, param, value)
	default:
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Unknown property %s", param.Name()))
	}
}

func (e *LivekitBin) GetProperty(instance *glib.Object, id uint) *glib.Value {
	self := gst.ToGstBin(instance)
	param := properties[id]
	switch param.Name() {
	case "sid":
		var sid string
		if e.Is(RoomStateJoined) {
			sid = e.room.SID()
		}
		return stringPropGetter(&sid)(self, param)
	case "room-name":
		var roomName string
		if e.Is(RoomStateJoined) {
			roomName = e.room.Name()
		}
		return stringPropGetter(&roomName)(self, param)
	case "ws-url":
		return stringPropGetter(&e.wsURL)(self, param)
	case "token":
		return stringPropGetter(&e.token)(self, param)
	case "participant-identity":
		identity := e.defaultParticipantIdentity
		if e.room.LocalParticipant != nil {
			identity = e.room.LocalParticipant.Identity()
		}
		return stringPropGetter(&identity)(self, param)
	case "participant-name":
		name := e.defaultParticipantName
		if e.room.LocalParticipant != nil {
			name = e.room.LocalParticipant.Name()
		}
		return stringPropGetter(&name)(self, param)
	case "participant-attributes":
		attributes := e.defaultParticipantAttributes
		if e.room.LocalParticipant != nil {
			attributes = e.room.LocalParticipant.Attributes()
		}
		structure := gst.NewStructure("participant-attributes")
		for k, v := range attributes {
			if err := structure.SetValue(k, v); err != nil {
				self.Log(CAT, gst.LevelError, fmt.Sprintf("Error setting participant attribute %s: %v", k, err))
			}
		}
		value, err := glib.GValue(structure)
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error getting participant-attributes property value: %v", err))
			return nil
		}
		return value
	case "max-active-participants":
		value, err := glib.GValue(e.maxActiveParticipants)
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error getting max-active-participants property value: %v", err))
			return nil
		}
		return value
	case "microphone":
		return boolPropGetter(&e.microphone)(self, param)
	case "microphone-mime-type":
		return stringPropGetter(&e.microphoneMimeType)(self, param)
	case "camera":
		return boolPropGetter(&e.camera)(self, param)
	case "camera-mime-type":
		return stringPropGetter(&e.cameraMimeType)(self, param)
	case "screenshare":
		return boolPropGetter(&e.screenshare)(self, param)
	case "screenshare-mime-type":
		return stringPropGetter(&e.screenshareMimeType)(self, param)
	case "screenshare-audio":
		return boolPropGetter(&e.screenshareAudio)(self, param)
	case "screenshare-audio-mime-type":
		return stringPropGetter(&e.screenshareAudioMimeType)(self, param)
	default:
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Unknown property %s", param.Name()))
		return nil
	}
}
