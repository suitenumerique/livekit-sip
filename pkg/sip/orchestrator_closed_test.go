package sip

import (
	"errors"
	"testing"
)

func TestOrchestratorRejectsSDPAfterClose(t *testing.T) {
	o := &MediaOrchestrator{state: MediaStateStarted}
	if err := o.okStates(MediaStateStarted); err != nil {
		t.Fatalf("open orchestrator: unexpected error %v", err)
	}
	o.closed.Store(true)
	if err := o.okStates(MediaStateStarted); !errors.Is(err, ErrMediaClosed) {
		t.Fatalf("closed orchestrator: got %v, want ErrMediaClosed", err)
	}
	if _, err := o.NewOffer(); !errors.Is(err, ErrMediaClosed) {
		t.Fatalf("NewOffer on closed orchestrator: got %v, want ErrMediaClosed", err)
	}
}
