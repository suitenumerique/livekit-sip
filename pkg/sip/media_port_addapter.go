package sip

import (
	"sync"

	"github.com/livekit/media-sdk"
	"github.com/livekit/media-sdk/rtp"
	sdpv2 "github.com/livekit/media-sdk/sdp/v2"
)

func NewAudioInfo(mp *MediaPort) *MediaPortAdapter {
	return &MediaPortAdapter{
		mp: mp,
	}
}

type MediaPortAdapter struct {
	mu    sync.Mutex
	mp    *MediaPort
	media *sdpv2.SDPMedia
}

// SetMedia implements AudioInfo.
func (m *MediaPortAdapter) SetMedia(media *sdpv2.SDPMedia) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.media = media
}

// AvailableCodecs implements AudioInfo.
func (m *MediaPortAdapter) AvailableCodecs() []*sdpv2.Codec {
	codecs := media.EnabledCodecs()
	sdpCodecs := make([]*sdpv2.Codec, 0, len(codecs))
	for _, c := range codecs {
		_, ok := c.(rtp.AudioCodec)
		if !ok {
			continue
		}
		codec, err := (&sdpv2.Codec{}).Builder().SetCodec(c).Build()
		if err != nil {
			continue
		}
		sdpCodecs = append(sdpCodecs, codec)
	}

	return sdpCodecs
}

// Codec implements AudioInfo.
func (m *MediaPortAdapter) Codec() *sdpv2.Codec {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.media == nil {
		return nil
	}
	return m.media.Codec
}

// Port implements AudioInfo.
func (m *MediaPortAdapter) Port() uint16 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return uint16(m.mp.Port())
}

var _ AudioInfo = (*MediaPortAdapter)(nil)
