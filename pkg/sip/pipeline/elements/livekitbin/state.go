package livekitbin

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
)

type RoomState int64

const (
	RoomStateNone    RoomState = 0
	RoomStateJoined  RoomState = 2
	RoomStateJoining RoomState = 4
	RoomStateClosed  RoomState = 16
)

var ErrRoomClosed = fmt.Errorf("room closed")

func (s RoomState) String() string {
	states := []string{}

	if s == 0 {
		return "none"
	}

	if s&RoomStateJoined != 0 {
		states = append(states, "joined")
	}
	if s&RoomStateJoining != 0 {
		states = append(states, "joining")
	}
	if s&RoomStateClosed != 0 {
		states = append(states, "closed")
	}

	if len(states) == 0 {
		return "unknown"
	}

	return strings.Join(states, "|")
}

type state struct {
	mu    sync.Mutex
	state atomic.Int64
	cond  *sync.Cond
	wg    sync.WaitGroup
}

func (s *state) Is(state RoomState) bool {
	return (s.state.Load() & int64(state)) != 0
}

func (s *state) IsAll(state RoomState) bool {
	return (s.state.Load() & int64(state)) == int64(state)
}

func (s *state) Set(state RoomState) RoomState {
	s.mu.Lock()
	old := s.state.Or(int64(state))
	s.mu.Unlock()
	s.cond.Broadcast()
	return RoomState(old)
}

func (s *state) Unset(state RoomState) RoomState {
	s.mu.Lock()
	old := s.state.And(^int64(state))
	s.mu.Unlock()
	s.cond.Broadcast()
	return RoomState(old)
}

func (s *state) Wait(state RoomState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for (s.state.Load()&int64(state)) == 0 && (s.state.Load()&int64(RoomStateClosed)) == 0 {
		s.cond.Wait()
	}
	if (s.state.Load() & int64(state)) != 0 {
		return nil
	}
	return ErrRoomClosed
}
