package sipbin

import (
	"testing"
	"time"

	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/livekit"
	"github.com/stretchr/testify/require"
)

// The rtpbin asks for the payload type map from its streaming threads while
// e.mu may be held by a send pad release waiting for those same threads. The
// handler must answer without e.mu, or the GLib main loop deadlocks with the
// streaming thread (production freeze of 2026-09-17).
func TestRequestPtMapDoesNotNeedStateLock(t *testing.T) {
	e := &SipBin{}
	for i := range e.PtMap {
		e.PtMap[i] = make(map[uint8]*gst.Caps)
	}
	caps := gst.NewCapsFromString("application/x-rtp,media=video,payload=96,encoding-name=H264,clock-rate=90000")
	e.PtMap[livekit.TrackSource_CAMERA][96] = caps

	e.mu.Lock()
	defer e.mu.Unlock()

	got := make(chan *gst.Caps, 1)
	go func() { got <- e.onRtpBinRequestPtMap(nil, int(livekit.TrackSource_CAMERA), 96) }()
	select {
	case c := <-got:
		require.NotNil(t, c)
		require.True(t, c.IsEqual(caps))
	case <-time.After(2 * time.Second):
		t.Fatal("request-pt-map blocked on e.mu")
	}
}

// Same requirement for the track lookup the rtpbin callbacks do on a new
// receive pad.
func TestTrackLookupDoesNotNeedStateLock(t *testing.T) {
	e := &SipBin{}
	track := &SipTrack{Kind: livekit.TrackSource_CAMERA}
	e.Tracks[livekit.TrackSource_CAMERA] = track

	e.mu.Lock()
	defer e.mu.Unlock()

	got := make(chan *SipTrack, 1)
	go func() { ti, _ := e.trackAndRtpBin(livekit.TrackSource_CAMERA); got <- ti }()
	select {
	case ti := <-got:
		require.Same(t, track, ti)
	case <-time.After(2 * time.Second):
		t.Fatal("track lookup blocked on e.mu")
	}
}
