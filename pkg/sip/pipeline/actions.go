package pipeline

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/go-gst/go-gst/gst"
	"golang.org/x/sys/unix"
)

func (p *Pipeline) EmitOfferSDP(offer string) (string, error) {
	res, err := p.SipBin.Emit("offer-sdp", offer)
	if err != nil {
		return "", fmt.Errorf("failed to emit offer-sdp: %w", err)
	}
	answer, ok := res.(string)
	if !ok {
		return "", fmt.Errorf("offer-sdp did not return a string")
	}
	return answer, nil
}

func (p *Pipeline) EmitAnswerSDP(answer string) error {
	if _, err := p.SipBin.Emit("answer-sdp", answer); err != nil {
		return fmt.Errorf("failed to emit answer-sdp: %w", err)
	}
	return nil
}

func (p *Pipeline) EmitAckSDP(sdp string) error {
	if _, err := p.SipBin.Emit("ack-sdp", sdp); err != nil {
		return fmt.Errorf("failed to emit ack-sdp: %w", err)
	}
	return nil
}

func (p *Pipeline) SendOfferCh() <-chan string {
	return p.SipIo.sendOfferCh
}

func (p *Pipeline) ConnectRoom(wsUrl, token string, attributes map[string]string) error {
	attr := gst.NewStructure("participant-attributes")

	for k, v := range attributes {
		if err := attr.SetValue(k, v); err != nil {
			p.Log.Warnw("failed to set participant attribute", err, "key", k, "value", v)
		}
	}

	if err := p.WebrtcIo.LivekitBin.SetProperty("participant-attributes", attr); err != nil {
		return fmt.Errorf("failed to set participant attributes: %w", err)
	}

	p.Log.Infow("Setting room options", "wsUrl", wsUrl)
	if err := p.WebrtcIo.LivekitBin.SetProperty("ws-url", wsUrl); err != nil {
		return fmt.Errorf("failed to set ws-url property: %w", err)
	}
	if err := p.WebrtcIo.LivekitBin.SetProperty("token", token); err != nil {
		return fmt.Errorf("failed to set token property: %w", err)
	}

	success := make(chan bool, 1)
	go func() {
		select {
		case <-p.WebrtcIo.Connected():
			success <- true
		case <-p.WebrtcIo.Closed():
			success <- false
		}
	}()

	if _, err := p.WebrtcIo.LivekitBin.Emit("connect"); err != nil {
		return fmt.Errorf("failed to emit connect signal: %v", err)
	}

	ok := <-success
	if !ok {
		return fmt.Errorf("failed to join room")
	}

	if err := p.WebrtcIo.LivekitBin.SetProperty("participant-attributes", attr); err != nil {
		return fmt.Errorf("failed to set participant attributes: %w", err)
	}

	p.Log.Infow("Joined room successfully", "wsUrl", wsUrl)

	return nil
}

func (p *Pipeline) PlayAudio(ctx context.Context, fd int) error {
	srcFd, err := unix.Open(
		fmt.Sprintf("/proc/self/fd/%d", fd),
		unix.O_RDONLY|unix.O_CLOEXEC,
		0,
	)
	if err != nil {
		return fmt.Errorf("failed to open per-call fd from master fd %d: %w", fd, err)
	}

	var pfds [2]int
	if err := unix.Pipe2(pfds[:], unix.O_CLOEXEC); err != nil {
		unix.Close(srcFd)
		return fmt.Errorf("pipe2: %w", err)
	}
	pipeR, pipeW := pfds[0], pfds[1]

	src := os.NewFile(uintptr(srcFd), "memfd-reader")
	wr := os.NewFile(uintptr(pipeW), "play-pipe-w")
	go func() {
		defer src.Close()
		defer wr.Close()

		if _, err := io.Copy(wr, src); err != nil {
			p.Log.Errorw("failed to copy audio data to pipe", err)
		}
	}()

	var PlayErr error
	done := make(chan struct{})
	go func() {
		if _, err := p.IOManager.LivekitController.Emit("play-audio-fd", pipeR); err != nil {
			PlayErr = fmt.Errorf("failed to emit play-audio-fd: %w", err)
		}
		p.Log.Debugw("play-audio-fd ended", "fd", pipeR, "err", PlayErr)
		close(done)
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return PlayErr
	}
}

func (p *Pipeline) SetContext(ctx *gst.Context) {
	if p == nil || p.pipeline == nil {
		return
	}
	p.pipeline.SetContext(ctx)
}
