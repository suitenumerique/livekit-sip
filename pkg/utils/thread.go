package utils

import (
	"context"
	"fmt"

	"github.com/frostbyte73/core"
)

type safeFunc struct {
	fn   func()
	done chan struct{}
}

type ThreadSafe struct {
	ctx     context.Context
	cancel  context.CancelFunc
	ch      chan safeFunc
	started core.Fuse
	stopped core.Fuse
}

func NewThreadSafe(ctx context.Context) *ThreadSafe {
	ctx, cancel := context.WithCancel(ctx)
	ts := &ThreadSafe{
		ctx:    ctx,
		cancel: cancel,
		ch:     make(chan safeFunc, 1),
	}

	return ts
}

func (t *ThreadSafe) Start() error {
	if !t.started.Break() {
		return fmt.Errorf("thread safe: already started")
	}
	go func() {
		defer t.stopped.Break()
		for {
			select {
			case sf := <-t.ch:
				if sf.fn != nil {
					sf.fn()
				}
				close(sf.done)
			case <-t.ctx.Done():
				return
			}
		}
	}()
	return nil
}

func (t *ThreadSafe) Stop() error {
	if !t.stopped.Break() {
		return fmt.Errorf("thread safe: already stopped")
	}
	t.cancel()
	<-t.stopped.Watch()
	return nil
}

func (t *ThreadSafe) Do(f func()) error {
	done, err := t.ADo(f)
	if err != nil {
		return err
	}
	<-done
	return nil
}

func (t *ThreadSafe) ADo(f func()) (chan struct{}, error) {
	if !t.started.IsBroken() {
		return nil, fmt.Errorf("thread safe: not started")
	}
	if t.stopped.IsBroken() {
		return nil, fmt.Errorf("thread safe: already stopped")
	}

	sf := safeFunc{
		fn:   f,
		done: make(chan struct{}),
	}

	t.ch <- sf

	return sf.done, nil
}
