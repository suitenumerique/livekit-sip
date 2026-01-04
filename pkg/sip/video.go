package sip

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"runtime"
	"syscall"
	"time"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/media-sdk/h264"
	mrtp "github.com/livekit/media-sdk/rtp"
	sdpv1 "github.com/livekit/media-sdk/sdp"
	sdpv2 "github.com/livekit/media-sdk/sdp/v2"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/sip/pkg/sip/pipeline"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/sinkwriter"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/sourcereader"
)

var mainLoop *glib.MainLoop

func init() {
	gst.Init(nil)
	if !sourcereader.Register() {
		panic("failed to register sourcereader element")
	}

	if !sinkwriter.Register() {
		panic("failed to register sinkwriter element")
	}

	mainLoop = glib.NewMainLoop(glib.MainContextDefault(), false)
	_ = mainLoop
}

type SipPipeline interface {
	pipeline.GstPipeline
	SipIO(rtp, rtcp net.Conn, pt uint8) error
}

type PipelineFactory interface {
	CreateVideoPipeline(opts *MediaOptions) (SipPipeline, error)
}

type VideoStatus int

const (
	VideoStatusClosed VideoStatus = iota
	VideoStatusStopped
	VideoStatusReady
	VideoStatusStarted
)

func (vs VideoStatus) String() string {
	switch vs {
	case VideoStatusClosed:
		return "closed"
	case VideoStatusStopped:
		return "stopped"
	case VideoStatusStarted:
		return "started"
	default:
		return "unknown"
	}
}

func NewVideoManager(log logger.Logger, ctx context.Context, opts *MediaOptions, factory PipelineFactory) (*VideoManager, error) {
	bindIP := opts.IPLocal
	if !bindIP.IsValid() {
		bindIP = opts.IP
	}
	rtpConn, rtcpConn, err := mrtp.ListenUDPPortPair(opts.Ports.Start, opts.Ports.End, bindIP)
	if err != nil {
		return nil, fmt.Errorf("failed to listen on UDP port pair for RTP/RTCP: bindIP=%s, portRange=%d-%d, advertiseIP=%s: %w",
			bindIP, opts.Ports.Start, opts.Ports.End, opts.IP, err)
	}

	v := &VideoManager{
		log:      log,
		ctx:      ctx,
		opts:     opts,
		rtpConn:  newUDPConn(log.WithComponent("video-rtp"), rtpConn),
		rtcpConn: newUDPConn(log.WithComponent("video-rtcp"), rtcpConn),
		factory:  factory,
		status:   VideoStatusStopped,
	}

	v.log.Infow("video manager created")

	return v, nil
}

type VideoManager struct {
	log      logger.Logger
	ctx      context.Context
	opts     *MediaOptions
	rtpConn  *udpConn
	rtcpConn *udpConn
	status   VideoStatus
	pipeline SipPipeline
	Media    *sdpv2.SDPMedia
	factory  PipelineFactory
}

func (v *VideoManager) RtpPort() int {
	return v.rtpConn.LocalAddr().(*net.UDPAddr).Port
}

func (v *VideoManager) RtcpPort() int {
	return v.rtcpConn.LocalAddr().(*net.UDPAddr).Port
}

func (v *VideoManager) Close() error {
	if v.status == VideoStatusClosed {
		return fmt.Errorf("video manager already closed")
	}
	v.log.Debugw("closing video manager")
	if err := v.stop(); err != nil {
		return fmt.Errorf("failed to stop video manager: %w", err)
	}
	v.rtpConn.Close()
	v.rtcpConn.Close()
	v.status = VideoStatusClosed
	return nil
}

func (v *VideoManager) Direction() sdpv2.Direction {
	if v.Media == nil {
		return sdpv2.DirectionInactive
	}
	return v.Media.Direction
}

func (v *VideoManager) Codec() *sdpv2.Codec {
	if v.Media == nil {
		return nil
	}
	return v.Media.Codec
}

func (v *VideoManager) Status() VideoStatus {
	return v.status
}

func (v *VideoManager) SupportedCodecs() []*sdpv2.Codec {
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

func isMedia(media *sdpv2.SDPMedia) bool {
	if media == nil {
		return false
	}
	if media.Port == 0 {
		return false
	}
	if media.Disabled {
		return false
	}
	if media.Codec == nil {
		return false
	}
	return true
}

func (v *VideoManager) mediaOK(newMedia *sdpv2.SDPMedia) bool {
	isOld := isMedia(v.Media)
	isNew := isMedia(newMedia)
	if !isOld && !isNew {
		return true
	}
	if !isOld || !isNew {
		return false
	}
	if v.Media.Port != newMedia.Port {
		return false
	}
	if v.Media.RTCPPort != newMedia.RTCPPort {
		return false
	}
	if v.Media.Direction != newMedia.Direction {
		return false
	}
	if v.Media.Codec.Name != newMedia.Codec.Name {
		return false
	}
	return true
}

type NoCloseConn struct {
	net.Conn
}

func (n *NoCloseConn) Close() error {
	return n.SetDeadline(time.Now())
}

type ReconcileStatus int

const (
	ReconcileStatusUnchanged ReconcileStatus = iota
	ReconcileStatusCreated
	ReconcileStatusUpdated
	ReconcileStatusStopped
)

func (v *VideoManager) Reconcile(remote netip.Addr, media *sdpv2.SDPMedia) (ReconcileStatus, error) {
	if v.status == VideoStatusClosed {
		return ReconcileStatusUnchanged, fmt.Errorf("video manager is closed")
	}

	if v.mediaOK(media) {
		v.log.Debugw("video media unchanged, no reconciliation needed", "oldMedia", v.Media, "newMedia", media)

		// Check if PayloadType changed (same codec, different PT)
		if v.status == VideoStatusStarted && v.Media != nil && v.Media.Codec != nil &&
			v.Media.Codec.PayloadType != media.Codec.PayloadType {
			v.log.Infow("PayloadType changed, reconfiguring SipIO",
				"oldPT", v.Media.Codec.PayloadType,
				"newPT", media.Codec.PayloadType)
			if err := v.pipeline.SipIO(&safeUDPConn{udpConn: v.rtpConn}, &safeUDPConn{udpConn: v.rtcpConn}, media.Codec.PayloadType); err != nil {
				v.log.Warnw("failed to update SipIO with new PayloadType", err)
			}
		}

		v.Media = media
		return ReconcileStatusUnchanged, nil
	}

	if !isMedia(media) {
		v.log.Debugw("video media disabled, stopping video manager", "media", media)
		v.Media = nil
		return ReconcileStatusStopped, v.stop()
	}

	rs := ReconcileStatusUpdated

	if init, err := v.resetPipeline(); err != nil {
		return ReconcileStatusUnchanged, fmt.Errorf("failed to reset GStreamer pipeline: %w", err)
	} else if init {
		rs = ReconcileStatusCreated
	}

	v.log.Infow("video setup", "remote", remote.String(), "rtp_port", v.RtpPort(), "rtcp_port", v.RtcpPort(), "codec", media.Codec, "direction", media.Direction)

	v.rtpConn.SetDst(netip.AddrPortFrom(remote, media.Port))
	v.rtcpConn.SetDst(netip.AddrPortFrom(remote, media.RTCPPort))

	v.Media = media
	v.status = VideoStatusReady

	return rs, nil
}

func (v *VideoManager) Start() error {
	if v.status == VideoStatusStarted {
		return nil
	}

	if err := v.pipeline.SipIO(&safeUDPConn{udpConn: v.rtpConn}, &safeUDPConn{udpConn: v.rtcpConn}, v.Media.Codec.PayloadType); err != nil {
		v.log.Errorw("failed to configure SIP IO", err)
		return fmt.Errorf("failed to configure SIP IO: %w", err)
	}

	v.status = VideoStatusStarted

	return nil
}

func (v *VideoManager) resetPipeline() (bool, error) {
	init := true
	v.log.Debugw("resetting video pipeline")
	if v.pipeline != nil {
		init = false
		v.log.Debugw("closing existing GStreamer pipeline")
		if err := v.pipeline.Close(); err != nil {
			v.log.Errorw("failed to close GStreamer pipeline, going to leak it", err)
		}
		v.pipeline = nil
		v.log.Debugw("existing GStreamer pipeline closed")
	}

	v.log.Debugw("creating new GStreamer pipeline")
	pipeline, err := v.factory.CreateVideoPipeline(v.opts)
	if err != nil {
		return init, fmt.Errorf("failed to create GStreamer pipeline: %w", err)
	}
	v.pipeline = pipeline
	v.log.Debugw("new GStreamer pipeline created")

	v.log.Debugw("starting video pipeline")
	if err := v.pipeline.SetState(gst.StatePaused); err != nil {
		return init, fmt.Errorf("failed to set GStreamer pipeline to paused: %w", err)
	}

	return init, nil
}

var pid = os.Getpid()

func (v *VideoManager) stop() error {
	v.log.Debugw("stopping video manager")

	if v.status == VideoStatusStopped {
		v.log.Debugw("video manager already stopped")
		return nil
	}

	v.Media = nil

	if v.pipeline != nil {
		if err := v.pipeline.Close(); err != nil {
			return fmt.Errorf("failed to set GStreamer pipeline to null: %w", err)
		}
		v.pipeline = nil
		time.Sleep(100 * time.Millisecond)
		runtime.GC()
		time.Sleep(100 * time.Millisecond)
		runtime.GC()

		syscall.Kill(pid, syscall.SIGUSR1)

		time.Sleep(1 * time.Second)
	}

	v.status = VideoStatusStopped

	return nil
}

type safeUDPConn struct {
	*udpConn
}

func (s *safeUDPConn) Read(b []byte) (int, error) {
	n, err := s.udpConn.Read(b)
	if err != nil {
		if errors.Is(err, net.ErrClosed) {
			return 0, io.EOF
		}
		if nerr, ok := err.(net.Error); ok && nerr.Timeout() {
			return 0, io.EOF
		}
		return n, err
	}
	return n, nil
}

func (s *safeUDPConn) Close() error {
	s.udpConn.SetReadDeadline(time.Now().Add(-1 * time.Second))
	return nil
}
