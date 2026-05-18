package sip

import (
	"context"
	"fmt"

	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/sip"
	"github.com/livekit/sip/pkg/config"
)

func (o *MediaOrchestrator) Connect(conf *config.Config, rconf RoomConfig) error {
	if rconf.WsUrl == "" {
		rconf.WsUrl = conf.WsUrl
	}
	partConf := rconf.Participant
	if rconf.Token == "" {
		// TODO: Remove this code path, always sign tokens on LiveKit server.
		//       For now, match Cloud behavior and do not send extra attrs in the token.
		tokenAttrs := make(map[string]string, len(partConf.Attributes))
		for _, k := range []string{
			livekit.AttrSIPCallID,
			livekit.AttrSIPTrunkID,
			livekit.AttrSIPDispatchRuleID,
			livekit.AttrSIPTrunkNumber,
			livekit.AttrSIPPhoneNumber,
		} {
			if v, ok := partConf.Attributes[k]; ok {
				tokenAttrs[k] = v
			}
		}
		var err error
		rconf.Token, err = sip.BuildSIPToken(sip.SIPTokenParams{
			APIKey:                conf.ApiKey,
			APISecret:             conf.ApiSecret,
			RoomName:              rconf.RoomName,
			ParticipantIdentity:   partConf.Identity,
			ParticipantName:       partConf.Name,
			ParticipantMetadata:   partConf.Metadata,
			ParticipantAttributes: tokenAttrs,
			RoomPreset:            rconf.RoomPreset,
			RoomConfig:            rconf.RoomConfig,
		})
		if err != nil {
			return err
		}
	}
	return o.pipeline.ConnectRoom(rconf.WsUrl, rconf.Token, rconf.Participant.Attributes)
}

func (o *MediaOrchestrator) Closed() <-chan struct{} {
	return o.pipeline.WebrtcIo.Closed()
}

func (o *MediaOrchestrator) Subscribed() <-chan struct{} {
	return o.pipeline.WebrtcIo.Connected()
}

func (o *MediaOrchestrator) Attributes() map[string]string {
	attr := make(map[string]string)
	attrStruct, err := o.pipeline.WebrtcIo.LivekitBin.GetProperty("participant-attributes")
	if err != nil {
		o.log.Warnw("failed to get participant attributes", err)
		return attr
	}
	attrStructVal, ok := attrStruct.(*gst.Structure)
	if !ok {
		o.log.Warnw("participant attributes is not a gst.Structure", nil, "value", attrStruct)
		return attr
	}
	for k, v := range attrStructVal.Values() {
		strVal, ok := v.(string)
		if !ok {
			o.log.Warnw("participant attribute value is not a string", nil, "key", k, "value", v)
			continue
		}
		attr[k] = strVal
	}
	return attr
}

func (o *MediaOrchestrator) SetAttributes(attributes map[string]string) error {
	attr := gst.NewStructure("participant-attributes")

	for k, v := range attributes {
		if err := attr.SetValue(k, v); err != nil {
			o.log.Warnw("failed to set participant attribute", err, "key", k, "value", v)
		}
	}

	if o.pipeline.WebrtcIo == nil || o.pipeline.WebrtcIo.LivekitBin == nil {
		return fmt.Errorf("livekit bin is not initialized")
	}
	if err := o.pipeline.WebrtcIo.LivekitBin.SetProperty("participant-attributes", attr); err != nil {
		return err
	}
	return nil
}

func (o *MediaOrchestrator) SID() string {
	sidVal, err := o.pipeline.WebrtcIo.LivekitBin.GetProperty("sid")
	if err != nil {
		o.log.Warnw("failed to get sid property", err)
		return ""
	}
	sid, ok := sidVal.(string)
	if !ok {
		o.log.Warnw("sid property is not a string", nil, "value", sidVal)
		return ""
	}
	return sid
}

func (o *MediaOrchestrator) RoomName() string {
	roomNameVal, err := o.pipeline.WebrtcIo.LivekitBin.GetProperty("room-name")
	if err != nil {
		o.log.Warnw("failed to get room-name property", err)
		return ""
	}
	roomName, ok := roomNameVal.(string)
	if !ok {
		o.log.Warnw("room-name property is not a string", nil, "value", roomNameVal)
		return ""
	}
	return roomName
}

func (o *MediaOrchestrator) PlayAudio(ctx context.Context, fd int) error {
	return o.pipeline.PlayAudio(ctx, fd)
}
