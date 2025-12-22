package event

import (
	"context"
	"fmt"
	"reflect"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/livekit/protocol/logger"
)

func NewEventLoop(ctx context.Context, log logger.Logger) *EventLoop {
	ctx, cancel := context.WithCancel(ctx)
	return &EventLoop{
		ctx:      ctx,
		cancel:   cancel,
		register: make(chan CallbackHandler, 10),
		log:      log,
	}
}

type CallbackHandler struct {
	loop *EventLoop
	done reflect.Value
	cb   reflect.Value
	args reflect.Value
	ret  reflect.Value
	inT  int
	outT int
	mu   *sync.Mutex
}

type EventLoop struct {
	mu        sync.Mutex
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	running   atomic.Bool
	register  chan CallbackHandler
	callbacks []CallbackHandler
	log       logger.Logger
}

func (loop *EventLoop) Stop() {
	loop.cancel()
	loop.wg.Wait()
	loop.callbacks = nil
	loop.wg = sync.WaitGroup{}
}

func (loop *EventLoop) Run() {
	if loop.running.Swap(true) {
		loop.log.Debugw("EventLoop: already running")
		return
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	loop.wg.Add(1)
	defer loop.wg.Done()
	for {
		loop.log.Debugw("EventLoop: waiting for events...")
		loop.mu.Lock()
		cases := make([]reflect.SelectCase, len(loop.callbacks)*2+2)
		cases[0] = reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(loop.ctx.Done())}
		cases[1] = reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(loop.register)}
		for i, handler := range loop.callbacks {
			pos := i*2 + 2
			cases[pos] = reflect.SelectCase{Dir: reflect.SelectRecv, Chan: handler.args}
			cases[pos+1] = reflect.SelectCase{Dir: reflect.SelectRecv, Chan: handler.done}
		}

		chosen, recv, ok := reflect.Select(cases)
		loop.log.Debugw("EventLoop: selected case", "case", chosen, "ok", ok)
		if chosen == 0 {
			loop.mu.Unlock()
			return
		}
		if !ok {
			loop.mu.Unlock()
			continue
		}
		if chosen == 1 {
			handler := recv.Interface().(CallbackHandler)
			loop.callbacks = append(loop.callbacks, handler)
			loop.mu.Unlock()
			continue
		}
		chosen -= 2
		if chosen%2 == 0 {
			// Message channel
			cb := loop.callbacks[chosen/2]

			args := make([]reflect.Value, cb.inT)
			for i := 0; i < cb.inT; i++ {
				args[i] = recv.Field(i)
			}
			loop.log.Debugw("EventLoop: invoking callback", "numArgs", cb.inT, "args", args)
			result := cb.cb.Call(args)
			loop.log.Debugw("EventLoop: callback returned", "numResults", cb.outT, "results", result)
			retStructFields := make([]reflect.StructField, cb.outT)
			for i := 0; i < cb.outT; i++ {
				retStructFields[i] = reflect.StructField{
					Name: fmt.Sprintf("Ret%d", i),
					Type: result[i].Type(),
				}
			}
			retStructType := reflect.StructOf(retStructFields)
			retStruct := reflect.New(retStructType).Elem()
			for i := 0; i < cb.outT; i++ {
				retStruct.Field(i).Set(result[i])
			}
			loop.log.Debugw("EventLoop: sending return struct", "retStruct", retStruct)
			cb.ret.Send(retStruct)
			loop.log.Debugw("EventLoop: return struct sent")
			// cb.mu.Unlock()

		} else {
			// Done channel
			idx := chosen / 2
			loop.callbacks = slices.Delete(loop.callbacks, idx, idx+1)
		}
		loop.mu.Unlock()
	}
}

func (loop *EventLoop) Register(cb CallbackHandler) {
	loop.register <- cb
}
