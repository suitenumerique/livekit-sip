package sip

import (
	"errors"
	"io"
	"sync/atomic"
	"time"

	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
)

type NopWriteCloser struct {
	io.Writer
}

func (n *NopWriteCloser) Close() error {
	return nil
}

type CallbackWriteCloser struct {
	io.Writer
	Callback func() error
}

func (c *CallbackWriteCloser) Close() error {
	var err error
	if c.Callback != nil {
		err = c.Callback()
		c.Callback = nil
	}

	if closer, ok := c.Writer.(io.Closer); ok {
		err = errors.Join(err, closer.Close())
	}

	return err
}

type CallbackReadCloser struct {
	io.Reader
	Callback func() error
}

func (c *CallbackReadCloser) Close() error {
	var err error
	if c.Callback != nil {
		err = c.Callback()
		c.Callback = nil
	}

	if closer, ok := c.Reader.(io.Closer); ok {
		err = errors.Join(err, closer.Close())
	}

	return err
}

func NewTrackAdapter(track *webrtc.TrackRemote, pub *lksdk.RemoteTrackPublication) *TrackAdapter {
	ta := &TrackAdapter{
		TrackRemote: track,
		pub:         pub,
	}
	return ta
}

type TrackAdapter struct {
	eof atomic.Bool
	*webrtc.TrackRemote
	pub *lksdk.RemoteTrackPublication
}

func (t *TrackAdapter) Read(p []byte) (n int, err error) {
	if t.eof.Load() {
		return 0, io.EOF
	}
	n, _, err = t.TrackRemote.Read(p)
	return n, err
}

func (t *TrackAdapter) Close() error {
	if t.eof.Swap(true) {
		return nil
	}
	// Set an immediate deadline to unblock any blocking Read() call.
	// This makes TrackRemote.Read() return with a timeout error immediately.
	t.TrackRemote.SetReadDeadline(time.Now())
	return nil
}

type RtcpWriter struct {
	pc *webrtc.PeerConnection
}

func (r *RtcpWriter) Write(p []byte) (n int, err error) {
	pkts, err := rtcp.Unmarshal(p)
	if err != nil {
		return 0, err
	}
	if err := r.pc.WriteRTCP(pkts); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (r *RtcpWriter) Close() error {
	return nil
}

func NewRtcpReader(pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) *RtcpReader {
	pipeReader, pipeWriter := io.Pipe()
	rr := &RtcpReader{
		pipeReader: pipeReader,
		pipeWriter: pipeWriter,
		pub:        pub,
		rp:         rp,
	}

	pub.OnRTCP(func(pkt rtcp.Packet) {
		var buf []byte
		b, err := pkt.Marshal()
		if err != nil {
			return
		}
		buf = append(buf, b...)
		_, err = rr.pipeWriter.Write(buf)
		if err != nil {
		}
	})

	return rr
}

type RtcpReader struct {
	pipeReader *io.PipeReader
	pipeWriter *io.PipeWriter
	pub        *lksdk.RemoteTrackPublication
	rp         *lksdk.RemoteParticipant
	deadline   atomic.Bool
}

func (r *RtcpReader) Read(p []byte) (n int, err error) {
	n, err = r.pipeReader.Read(p)
	if r.deadline.Load() {
		r.deadline.Store(false)
	}
	return n, err
}

func (r *RtcpReader) SetReadDeadline(t time.Time) error {
	r.deadline.Store(true)
	go func() {
		time.Sleep(time.Until(t))
		if r.deadline.Load() {
			r.pipeWriter.Write([]byte{})
		}
	}()
	return nil
}

func (r *RtcpReader) Close() error {
	r.pub.OnRTCP(nil)
	return errors.Join(r.pipeWriter.Close(), r.pipeReader.Close())
}

func NewTrackInput(track *webrtc.TrackRemote, pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) *TrackInput {
	ti := &TrackInput{
		RtpIn:  NewTrackAdapter(track, pub),
		RtcpIn: NewRtcpReader(pub, rp),
		RequestKeyframe: func() error {
			// Send RTCP PLI (Picture Loss Indication) to request keyframe from WebRTC sender
			rp.WritePLI(track.SSRC())
			return nil
		},
	}
	return ti
}

type WebrtcTrackInput struct {
	TrackInput
	SSRC uint32
}

type TrackInput struct {
	RtpIn          io.ReadCloser
	RtcpIn         io.ReadCloser
	RequestKeyframe func() error // Callback to request keyframe via RTCP PLI
}

type TrackOutput struct {
	RtpOut  io.WriteCloser
	RtcpOut io.WriteCloser
}
