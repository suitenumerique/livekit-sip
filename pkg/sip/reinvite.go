package sip

type SdpResult struct {
	SDP []byte
	Err error
}

type SipSdpInterface interface {
	OfferSDP(offer []byte) (answer chan SdpResult, err error)
	AnswerSDP(answer []byte) error
}

type MediaSdpInterface interface {
	OnOffer(offer []byte) (answer chan SdpResult, err error)
	OnAnswer(answer []byte) error
}
