package sip

import (
	"errors"
	"io"
	"sync"
	"sync/atomic"

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
	if c.Callback != nil {
		err := c.Callback()
		c.Callback = nil
		return err
	}
	return nil
}

func NewSwitchWriter() *SwitchWriter {
	return &SwitchWriter{}
}

type SwitchWriter struct {
	w atomic.Pointer[io.WriteCloser]
}

func (s *SwitchWriter) Write(p []byte) (n int, err error) {
	w := s.w.Load()
	if w == nil {
		return 0, nil
	}
	return (*w).Write(p)
}

func (s *SwitchWriter) Close() error {
	// Just clear the pointer, don't close the underlying writer.
	// The underlying writer (e.g., UDP connection) may be reused for screenshare restart.
	s.w.Store(nil)
	return nil
}

func (s *SwitchWriter) Swap(w io.WriteCloser) io.WriteCloser {
	var old *io.WriteCloser
	if w == nil {
		old = s.w.Swap(nil)
	} else {
		old = s.w.Swap(&w)
	}
	if old != nil {
		return *old
	}
	return nil
}

func NewSwitchReader() *SwitchReader {
	sr := &SwitchReader{}
	sr.b = sync.NewCond(&sr.mu)
	sr.closed.Store(false)
	return sr
}

type SwitchReader struct {
	r      atomic.Pointer[io.ReadCloser]
	b      *sync.Cond
	mu     sync.Mutex
	closed atomic.Bool
}

func (s *SwitchReader) Read(p []byte) (n int, err error) {
	r := s.r.Load()
	if r == nil {
		// If closed, return EOF immediately
		if s.closed.Load() {
			return 0, io.EOF
		}
		// Wait for a reader to be swapped in
		s.mu.Lock()
		s.b.Wait()
		s.mu.Unlock()
		// Check again if closed after waking up
		if s.closed.Load() {
			return 0, io.EOF
		}
		return s.Read(p)
	}
	return (*r).Read(p)
}

func (s *SwitchReader) Close() error {
	// Mark as closed first
	s.closed.Store(true)
	// Wake up any blocked readers
	s.mu.Lock()
	s.b.Broadcast()
	s.mu.Unlock()
	// Close the underlying reader if exists
	r := s.r.Load()
	if r != nil {
		return (*r).Close()
	}
	return nil
}

func (s *SwitchReader) Swap(r io.ReadCloser) io.ReadCloser {
	var old *io.ReadCloser
	if r == nil {
		old = s.r.Swap(nil)
	} else {
		old = s.r.Swap(&r)
		s.mu.Lock()
		s.b.Broadcast()
		s.mu.Unlock()
	}
	if old != nil {
		return *old
	}
	return nil
}

// Reset clears the closed flag and underlying reader so the SwitchReader can be reused.
// This is needed when the screenshare pipeline is restarted after being stopped.
func (s *SwitchReader) Reset() {
	s.closed.Store(false)
	// Clear the old reader so Read() will block until a new one is swapped in.
	// Without this, Read() would try to read from the old closed reader.
	s.r.Store(nil)
}

func NewTrackAdapter(track *webrtc.TrackRemote) *TrackAdapter {
	ta := &TrackAdapter{}
	ta.Store(track)
	return ta
}

type TrackAdapter struct {
	atomic.Pointer[webrtc.TrackRemote]
}

func (t *TrackAdapter) Read(p []byte) (n int, err error) {
	tr := t.Load()
	if tr == nil {
		return 0, io.EOF
	}
	n, _, err = (*tr).Read(p)
	return n, err
}

func (t *TrackAdapter) Close() error {
	t.Store(nil)
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
}

func (r *RtcpReader) Read(p []byte) (n int, err error) {
	return r.pipeReader.Read(p)
}

func (r *RtcpReader) Close() error {
	return errors.Join(r.pipeWriter.Close(), r.pipeReader.Close())
}

func NewTrackInput(track *webrtc.TrackRemote, pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) *TrackInput {
	ti := &TrackInput{
		RtpIn:  NewTrackAdapter(track),
		RtcpIn: NewRtcpReader(pub, rp),
	}
	return ti
}

type WebrtcTrackInput struct {
	TrackInput
	SSRC uint32
}

type TrackInput struct {
	RtpIn  io.ReadCloser
	RtcpIn io.ReadCloser
}

type TrackOutput struct {
	RtpOut  io.WriteCloser
	RtcpOut io.WriteCloser
}
