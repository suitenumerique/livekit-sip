package sipbin

import (
	"fmt"
	"sync"
)

var ErrTransactionClosed = fmt.Errorf("transaction closed")

type TransactionPendingKind int

const (
	TransactionPendingKindNone TransactionPendingKind = iota
	TransactionPendingKindAck
	TransactionPendingKindAnswer
)

func NewSipTransaction() *SipTransaction {
	t := &SipTransaction{}
	t.cond = sync.NewCond(&t.mu)
	t.pending = TransactionPendingKindNone
	return t
}

type SipTransaction struct {
	mu      sync.Mutex
	cond    *sync.Cond
	active  bool
	pending TransactionPendingKind
	closed  bool
}

func (t *SipTransaction) WaitReady() (unlock func(), err error) {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil, ErrTransactionClosed
	}
	for t.active {
		t.cond.Wait()
		if t.closed {
			t.mu.Unlock()
			return nil, ErrTransactionClosed
		}
	}
	t.active = true
	t.pending = TransactionPendingKindNone
	return func() {
		if t.pending == TransactionPendingKindNone {
			t.active = false
			t.cond.Broadcast()
		}
		t.mu.Unlock()
	}, nil
}

func (t *SipTransaction) SetPending(kind TransactionPendingKind) {
	// do we need to handle ack timeout here?
	t.pending = kind
}

func (t *SipTransaction) Ack(kind TransactionPendingKind) (unlock func(), err error) {
	t.mu.Lock()

	if t.pending != kind {
		t.mu.Unlock()
		return nil, fmt.Errorf("transaction pending kind mismatch: got %v, expected %v", t.pending, kind)
	}

	t.active = true
	t.pending = TransactionPendingKindNone
	return func() {
		if t.pending == TransactionPendingKindNone {
			t.active = false
			t.cond.Broadcast()
		}
		t.mu.Unlock()
	}, nil
}

func (t *SipTransaction) GetPending() (pending TransactionPendingKind, unlock func()) {
	t.mu.Lock()
	pending = t.pending
	return pending, func() {
		t.mu.Unlock()
	}
}

func (t *SipTransaction) Close() {
	t.mu.Lock()
	t.closed = true
	t.cond.Broadcast()
	t.mu.Unlock()
}
