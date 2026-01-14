package sip

import (
	"os"

	"github.com/livekit/protocol/logger"

	sdpv2 "github.com/livekit/media-sdk/sdp/v2"
)

var sipSDPDebug = os.Getenv("SIP_SDP_DEBUG") == "true"

func logSDPOffer(log logger.Logger, offer *sdpv2.SDP) {
	if !sipSDPDebug {
		return
	}

	log.Infow("[SDP] === OFFER RECEIVED ===",
		"address", offer.Addr,
	)

	logSDPMedia(log, "Offer Audio", offer.Audio)
	logSDPMedia(log, "Offer Video", offer.Video)
	logSDPMedia(log, "Offer Screenshare", offer.Screenshare)
	logSDPBfcp(log, "Offer BFCP", offer.BFCP)
}

func logSDPAnswer(log logger.Logger, answer *sdpv2.SDP) {
	if !sipSDPDebug {
		return
	}

	log.Infow("[SDP] === ANSWER GENERATED ===",
		"address", answer.Addr,
	)

	logSDPMedia(log, "Answer Audio", answer.Audio)
	logSDPMedia(log, "Answer Video", answer.Video)
	logSDPMedia(log, "Answer Screenshare", answer.Screenshare)
	logSDPBfcp(log, "Answer BFCP", answer.BFCP)
}

func logSDPReInviteOffer(log logger.Logger, offer *sdpv2.SDP) {
	if !sipSDPDebug {
		return
	}

	log.Infow("[SDP] === RE-INVITE OFFER RECEIVED ===",
		"address", offer.Addr,
	)

	logSDPMedia(log, "ReInvite Offer Audio", offer.Audio)
	logSDPMedia(log, "ReInvite Offer Video", offer.Video)
	logSDPMedia(log, "ReInvite Offer Screenshare", offer.Screenshare)
	logSDPBfcp(log, "ReInvite Offer BFCP", offer.BFCP)
}

func logSDPReInviteAnswer(log logger.Logger, answer *sdpv2.SDP) {
	if !sipSDPDebug {
		return
	}

	log.Infow("[SDP] === RE-INVITE ANSWER GENERATED ===",
		"address", answer.Addr,
	)

	logSDPMedia(log, "ReInvite Answer Audio", answer.Audio)
	logSDPMedia(log, "ReInvite Answer Video", answer.Video)
	logSDPMedia(log, "ReInvite Answer Screenshare", answer.Screenshare)
	logSDPBfcp(log, "ReInvite Answer BFCP", answer.BFCP)
}

func logSDPMedia(log logger.Logger, prefix string, m *sdpv2.SDPMedia) {
	if m == nil {
		return
	}

	log.Infow("[SDP] "+prefix,
		"port", m.Port,
		"rtcpPort", m.RTCPPort,
		"direction", m.Direction,
		"disabled", m.Disabled,
		"content", m.Content,
		"label", m.Label,
		"codecCount", len(m.Codecs),
	)

	for i, codec := range m.Codecs {
		log.Infow("[SDP] "+prefix+" Codec",
			"index", i,
			"payloadType", codec.PayloadType,
			"name", codec.Name,
			"clockRate", codec.ClockRate,
			"fmtp", codec.FMTP,
		)
	}

	if m.Codec != nil {
		log.Infow("[SDP] "+prefix+" SELECTED",
			"payloadType", m.Codec.PayloadType,
			"name", m.Codec.Name,
			"fmtp", m.Codec.FMTP,
		)
	}
}

func logSDPBfcp(log logger.Logger, prefix string, b *sdpv2.SDPBfcp) {
	if b == nil {
		return
	}

	log.Infow("[SDP] "+prefix,
		"port", b.Port,
		"disabled", b.Disabled,
		"proto", b.Proto,
		"setup", b.Setup,
		"connection", b.Connection,
		"floorctrl", b.FloorCtrl,
		"confid", b.ConfID,
		"userid", b.UserID,
		"floorid", b.FloorID,
		"mstreamid", b.MStreamID,
	)
}
