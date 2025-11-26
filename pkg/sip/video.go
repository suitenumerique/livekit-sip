package sip

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/media-sdk/h264"
	mrtp "github.com/livekit/media-sdk/rtp"
	sdpv1 "github.com/livekit/media-sdk/sdp"
	sdpv2 "github.com/livekit/media-sdk/sdp/v2"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/sip/pkg/sip/pipeline"
)

var mainLoop *glib.MainLoop

func init() {
	gst.Init(nil)

	mainLoop = glib.NewMainLoop(glib.MainContextDefault(), false)
	_ = mainLoop
}

type VideoStatus int

const (
	VideoStatusClosed VideoStatus = iota
	VideoStatusStopped
	VideoStatusReady
	VideoStatusStarted
)

func NewVideoManager(log logger.Logger, room *Room, opts *MediaOptions) (*VideoManager, error) {
	// Allocate RTP/RTCP port pair according to RFC 3550 (RTCP on RTP+1)
	rtpConn, rtcpConn, err := mrtp.ListenUDPPortPair(opts.Ports.Start, opts.Ports.End, opts.IP)
	if err != nil {
		return nil, fmt.Errorf("failed to listen on UDP port pair for RTP/RTCP: %w", err)
	}

	v := &VideoManager{
		VideoIO:  NewVideoIO(),
		log:      log,
		room:     room,
		opts:     opts,
		rtpConn:  newUDPConn(log.WithComponent("video-rtp"), rtpConn),
		rtcpConn: newUDPConn(log.WithComponent("video-rtcp"), rtcpConn),
		status:   VideoStatusStopped,
	}

	v.log.Infow("video manager created")

	return v, nil
}

type VideoManager struct {
	*VideoIO
	mu       sync.Mutex
	log      logger.Logger
	opts     *MediaOptions
	room     *Room
	rtpConn  *udpConn
	rtcpConn *udpConn
	pipeline *pipeline.GstPipeline
	codec    *sdpv2.Codec
	recv     bool
	send     bool
	status   VideoStatus
}

type VideoIO struct {
	sipRtpIn   *SwitchReader
	sipRtpOut  *SwitchWriter
	sipRtcpIn  *SwitchReader
	sipRtcpOut *SwitchWriter

	webrtcRtpOut  *SwitchWriter
	webrtcRtcpOut *SwitchWriter
}

func NewVideoIO() *VideoIO {
	return &VideoIO{
		sipRtpIn:      NewSwitchReader(),
		sipRtpOut:     NewSwitchWriter(),
		sipRtcpIn:     NewSwitchReader(),
		sipRtcpOut:    NewSwitchWriter(),
		webrtcRtpOut:  NewSwitchWriter(),
		webrtcRtcpOut: NewSwitchWriter(),
	}
}

func (v *VideoIO) Close() error {
	return errors.Join(
		v.sipRtpIn.Close(),
		v.sipRtpOut.Close(),
		v.sipRtcpIn.Close(),
		v.sipRtcpOut.Close(),
		v.webrtcRtpOut.Close(),
		v.webrtcRtcpOut.Close(),
	)
}

func (v *VideoManager) RtpPort() int {
	return v.rtpConn.LocalAddr().(*net.UDPAddr).Port
}

func (v *VideoManager) RtcpPort() int {
	return v.rtcpConn.LocalAddr().(*net.UDPAddr).Port
}

func (v *VideoManager) WebrtcTrackInput(ti *TrackInput, sid string, ssrc uint32) {
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.status != VideoStatusStarted {
		v.log.Warnw("video manager not started, cannot add WebRTC track input", nil, "status", v.status)
		return
	}

	v.log.Infow("WebRTC video track subscribed - connecting WebRTC→SIP pipeline",
		"hasRtpIn", ti.RtpIn != nil,
		"hasRtcpIn", ti.RtcpIn != nil)

	s, err := v.pipeline.AddWebRTCSourceToSelector(sid, ssrc)
	if err != nil {
		v.log.Errorw("failed to add WebRTC source to selector", err)
		return
	}

	webrtcRtpIn, err := NewGstWriter(s.WebrtcRtpAppSrc)
	if err != nil {
		v.log.Errorw("failed to create WebRTC RTP writer", err)
		return
	}
	go func() {
		v.Copy(webrtcRtpIn, ti.RtpIn)
		if err := v.pipeline.RemoveWebRTCSourceFromSelector(sid); err != nil {
			v.log.Errorw("failed to remove WebRTC source from selector", err)
		}
	}()

	// TODO: fix RTCP pipeline then enable this
	// webrtcRtcpIn, err := NewGstWriter(s.WebrtcRtcpAppSrc)
	// if err != nil {
	// 	v.log.Errorw("failed to create WebRTC RTCP reader", err)
	// 	return
	// }
	// go Copy(webrtcRtcpIn, ti.RtcpIn)
}

func (v *VideoManager) SwitchActiveWebrtcTrack(sid string) error {
	v.mu.Lock()
	defer v.mu.Unlock()

	// ssrc, ok := v.ssrcs[sid]
	// if !ok {
	// 	return fmt.Errorf("no SSRC found for sid %s", sid)
	// }
	// v.log.Debugw("switching active WebRTC video track", "sid", sid, "ssrc", ssrc)

	// pli := &rtcp.PictureLossIndication{
	// 	MediaSSRC:  ssrc, // The track we want a keyframe for
	// 	SenderSSRC: 0,    // Your local SSRC (0 is usually acceptable if unknown)
	// }

	// buf, err := pli.Marshal()
	// if err != nil {
	// 	return fmt.Errorf("failed to marshal PLI: %w", err)
	// }

	// _, err = v.webrtcRtcpOut.Write(buf)
	// if err != nil {
	// 	return fmt.Errorf("failed to send PLI RTCP packet: %w", err)
	// }

	if err := v.pipeline.SwitchWebRTCSelectorSource(sid); err != nil {
		return fmt.Errorf("failed to switch WebRTC selector source: %w", err)
	}

	return nil
}

func (v *VideoManager) WebrtcTrackOutput(to *TrackOutput) {
	v.log.Infow("WebRTC video track published - connecting SIP→WebRTC pipeline",
		"hasRtpOut", to.RtpOut != nil,
		"hasRtcpOut", to.RtcpOut != nil)

	if w := v.webrtcRtpOut.Swap(to.RtpOut); w != nil {
		_ = w.Close()
	}
	if w := v.webrtcRtcpOut.Swap(to.RtcpOut); w != nil {
		_ = w.Close()
	}
}

func (v *VideoManager) Close() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.status = VideoStatusClosed
	v.log.Debugw("closing video manager")
	if v.pipeline != nil {
		if err := v.pipeline.Close(); err != nil {
			return fmt.Errorf("failed to set GStreamer pipeline to null: %w", err)
		}
	}
	if err := v.VideoIO.Close(); err != nil {
		return fmt.Errorf("failed to close video IO: %w", err)
	}
	if err := v.rtpConn.Close(); err != nil {
		return fmt.Errorf("failed to close RTP connection: %w", err)
	}
	if err := v.rtcpConn.Close(); err != nil {
		return fmt.Errorf("failed to close RTCP connection: %w", err)
	}
	return nil
}

func (v *VideoManager) Status() VideoStatus {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.status
}

func (v *VideoManager) Direction() sdpv2.Direction {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.send && v.recv {
		return sdpv2.DirectionSendRecv
	}
	if v.send {
		return sdpv2.DirectionSendOnly
	}
	if v.recv {
		return sdpv2.DirectionRecvOnly
	}
	return sdpv2.DirectionInactive
}

func (v *VideoManager) Codec() *sdpv2.Codec {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.codec
}

func (v *VideoManager) SupportedCodecs() []*sdpv2.Codec {
	//TODO: make is dynamic.
	c := sdpv1.CodecByName(h264.SDPName)
	if c == nil {
		return []*sdpv2.Codec{}
	}
	codec, err := (&sdpv2.Codec{}).Builder().SetCodec(c).Build()
	if err != nil {
		return []*sdpv2.Codec{}
	}
	return []*sdpv2.Codec{
		codec,
	}
}

func (v *VideoManager) setupOutput(remote netip.Addr, media *sdpv2.SDPMedia, send bool) error {
	if !send {
		if w := v.sipRtpOut.Swap(nil); w != nil {
			_ = w.Close()
		}
		if w := v.sipRtcpOut.Swap(nil); w != nil {
			_ = w.Close()
		}

		v.rtpConn.SetDst(netip.AddrPort{})
		v.rtcpConn.SetDst(netip.AddrPort{})
		return nil
	}
	rtpAddr := netip.AddrPortFrom(remote, media.Port)
	v.rtpConn.SetDst(rtpAddr)

	rtcpAddr := netip.AddrPortFrom(remote, media.RTCPPort)
	v.rtcpConn.SetDst(rtcpAddr)

	v.log.Infow("setting up video send to SIP", "remote", remote.String(), "port", media.Port, "rtcp_port", media.RTCPPort)
	if w := v.sipRtpOut.Swap(v.rtpConn); w != nil {
		_ = w.Close()
	}
	if w := v.sipRtcpOut.Swap(v.rtcpConn); w != nil {
		_ = w.Close()
	}
	return nil
}

func (v *VideoManager) setupInput(remote netip.Addr, media *sdpv2.SDPMedia, recv bool) error {
	if !recv {
		if r := v.sipRtpIn.Swap(nil); r != nil {
			_ = r.Close()
		}
		if r := v.sipRtcpIn.Swap(nil); r != nil {
			_ = r.Close()
		}
		return nil
	}

	v.log.Infow("setting up video receive from SIP", "remote", remote.String(), "port", media.Port, "rtcp_port", media.RTCPPort)
	if r := v.sipRtpIn.Swap(v.rtpConn); r != nil {
		_ = r.Close()
	}
	if r := v.sipRtcpIn.Swap(v.rtcpConn); r != nil {
		_ = r.Close()
	}
	return nil
}

func (v *VideoManager) Setup(remote netip.Addr, media *sdpv2.SDPMedia) error {
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.status != VideoStatusStopped {
		return fmt.Errorf("video manager must be stopped to setup, current status: %d", v.status)
	}

	v.log.Debugw("setting up video manager", "media", media)

	if media.Codec == nil {
		return fmt.Errorf("no codec selected for video media")
	}
	v.codec = media.Codec

	if err := v.SetupGstPipeline(media); err != nil {
		return fmt.Errorf("failed to setup GStreamer pipeline: %w", err)
	}

	v.recv = media.Direction.IsRecv()
	v.send = media.Direction.IsSend()

	v.log.Infow("video setup", "send", v.send, "recv", v.recv, "remote", remote.String(), "rtp_port", v.RtpPort(), "rtcp_port", v.RtcpPort())

	if err := v.setupOutput(remote, media, v.recv); err != nil {
		return fmt.Errorf("failed to setup video input: %w", err)
	}

	if err := v.setupInput(remote, media, v.send); err != nil {
		return fmt.Errorf("failed to setup video output: %w", err)
	}

	v.status = VideoStatusReady

	return nil
}

func (v *VideoManager) Start() error {
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.status != VideoStatusReady {
		return fmt.Errorf("video manager must be ready to start, current status: %d", v.status)
	}

	v.log.Debugw("starting video manager")

	// Set status to Started BEFORE setting pipeline state to allow WebRTC tracks to be added
	// The pipeline state change to PLAYING may be ASYNC and wait for data (preroll)
	// But we need to accept WebRTC tracks for data to flow
	v.status = VideoStatusStarted

	if err := v.pipeline.SetState(gst.StatePlaying); err != nil {
		v.status = VideoStatusReady // Rollback on error
		return fmt.Errorf("failed to set GStreamer pipeline to playing: %w", err)
	}

	return nil
}

func (v *VideoManager) Stop() error {
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.status == VideoStatusStopped {
		return nil
	}

	if v.status != VideoStatusStarted {
		return fmt.Errorf("video manager must be started to stop, current status: %d", v.status)
	}

	v.log.Debugw("stopping video manager")

	v.codec = nil

	if err := v.pipeline.Close(); err != nil {
		return fmt.Errorf("failed to set GStreamer pipeline to null: %w", err)
	}

	v.recv = false
	v.send = false

	v.status = VideoStatusStopped
	return nil
}
