package sip

import (
	"fmt"
	"sync"

	sdpv2 "github.com/livekit/media-sdk/sdp/v2"
	"github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4"
)

type MediaOrchestrator struct {
	mu      sync.Mutex
	opts    *MediaOptions
	log     logger.Logger
	inbound *sipInbound
	room    *Room
	camera  *VideoManager
}

func NewMediaOrchestrator(log logger.Logger, inbound *sipInbound, room *Room, opts *MediaOptions) (*MediaOrchestrator, error) {
	o := &MediaOrchestrator{
		log:     log,
		inbound: inbound,
		room:    room,
		opts:    opts,
	}

	camera, err := NewVideoManager(log.WithComponent("camera"), room, opts)
	if err != nil {
		return nil, fmt.Errorf("could not create video manager: %w", err)
	}
	o.camera = camera
	o.room.OnCameraTrack(func(track *webrtc.TrackRemote, pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
		ti := NewTrackInput(track, pub, rp)
		o.camera.WebrtcTrackInput(ti, rp.SID(), uint32(track.SSRC()))
	})
	o.room.OnActiveSpeakersChanged(func(p []lksdk.Participant) {
		if len(p) == 0 {
			o.log.Debugw("no active speakers found")
			return
		}
		sid := p[0].SID()
		if err := o.camera.SwitchActiveWebrtcTrack(sid); err != nil {
			o.log.Warnw("could not switch active webrtc track", err, "sid", sid)
		}
	})

	return o, nil
}

func (o *MediaOrchestrator) Close() error {
	return nil
}

func (o *MediaOrchestrator) AnswerSDP(offer *sdpv2.SDP) (*sdpv2.SDP, error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	if offer.Video != nil {
		if err := offer.Video.SelectCodec(); err != nil {
			return nil, fmt.Errorf("could not select video codec: %w", err)
		}
	}

	if err := o.setupSDP(offer); err != nil {
		return nil, fmt.Errorf("could not setup sdp: %w", err)
	}

	answer, err := o.offerSDP(offer.Video != nil)
	if err != nil {
		return nil, fmt.Errorf("could not create answer sdp: %w", err)
	}

	return answer, nil
}

func (o *MediaOrchestrator) offerSDP(camera bool) (*sdpv2.SDP, error) {
	builder := (&sdpv2.SDP{}).Builder()

	builder.SetAddress(o.opts.IP)

	if camera {
		builder.SetVideo(func(b *sdpv2.SDPMediaBuilder) (*sdpv2.SDPMedia, error) {
			codec := o.camera.Codec()
			if codec == nil {
				for _, c := range o.camera.SupportedCodecs() {
					b.AddCodec(func(_ *sdpv2.CodecBuilder) (*sdpv2.Codec, error) {
						return c, nil
					}, false)
				}
			} else {
				b.AddCodec(func(_ *sdpv2.CodecBuilder) (*sdpv2.Codec, error) {
					return codec, nil
				}, true)
			}
			b.SetDisabled(o.camera.Status() != VideoStatusStarted)
			b.SetRTPPort(uint16(o.camera.RtpPort()))
			b.SetRTCPPort(uint16(o.camera.RtcpPort()))
			b.SetDirection(o.camera.Direction())
			return b.Build()
		})
	}

	offer, err := builder.Build()
	if err != nil {
		return nil, fmt.Errorf("could create a new sdp: %w", err)
	}

	return offer, nil
}

func (o *MediaOrchestrator) OfferSDP() (*sdpv2.SDP, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.offerSDP(true)
}

func (o *MediaOrchestrator) setupSDP(sdp *sdpv2.SDP) error {
	if err := o.camera.Stop(); err != nil {
		return fmt.Errorf("could not stop video manager: %w", err)
	}
	if err := o.room.StopCamera(); err != nil {
		return fmt.Errorf("could not stop room camera: %w", err)
	}

	if sdp.Video != nil && !sdp.Video.Disabled {
		if err := o.camera.Setup(sdp.Addr, sdp.Video); err != nil {
			return fmt.Errorf("could not setup video sdp: %w", err)
		}
	}

	return nil
}

func (o *MediaOrchestrator) SetupSDP(sdp *sdpv2.SDP) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.setupSDP(sdp)
}

func (o *MediaOrchestrator) Start() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.start()
}

func (o *MediaOrchestrator) start() error {
	if o.camera.Status() == VideoStatusReady {
		if err := o.camera.Start(); err != nil {
			return fmt.Errorf("could not start video manager: %w", err)
		}
		to, err := o.room.StartCamera()
		if err != nil {
			return fmt.Errorf("could not start room camera: %w", err)
		}
		o.camera.WebrtcTrackOutput(to)
	}
	return nil

}
