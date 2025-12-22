package sip

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"time"
)

type MediaLink struct {
	ctx     context.Context
	cancel  context.CancelFunc
	Dst     io.WriteCloser
	Src     io.ReadCloser
	running atomic.Bool
	ch      chan error
}

func NewMediaLink(ctx context.Context, dst io.WriteCloser, src io.ReadCloser) *MediaLink {
	cctx, cancel := context.WithCancel(ctx)
	return &MediaLink{
		ctx:    cctx,
		cancel: cancel,
		Dst:    dst,
		Src:    src,
		ch:     make(chan error, 1),
	}
}

func (m *MediaLink) stop() error {
	if !m.running.Swap(false) {
		return nil
	}
	m.cancel()

	t := time.After(time.Second * 5)
	select {
	case err, ok := <-m.ch:
		if !ok {
			return nil
		}
		if err == io.EOF {
			return nil
		}
		return err
	case <-t:
		return errors.New("timeout waiting for media link to stop")
	}
}

func (m *MediaLink) Close() error {
	if err := m.Src.Close(); err != nil {
		return fmt.Errorf("failed to close media link source: %w", err)
	}
	if err := m.Dst.Close(); err != nil {
		return fmt.Errorf("failed to close media link destination: %w", err)
	}

	return m.stop()
}

func (m *MediaLink) Start() error {
	if m.running.Swap(true) {
		return nil
	}
	go func() {
		buf := make([]byte, 1500)
		var err error
		var n int
		defer func() {
			if err != nil {
				m.ch <- err
			}
			close(m.ch)
		}()
		for {
			select {
			case <-m.ctx.Done():
				return
			default:
			}

			n, err = m.Src.Read(buf)
			if err != nil {
				return
			}

			select {
			case <-m.ctx.Done():
				return
			default:
			}

			_, err = m.Dst.Write(buf[:n])
			if err != nil {
				return
			}
		}
	}()

	return nil
}
