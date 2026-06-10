package sipbin

import (
	"fmt"
	"sync"
	"time"
)

var (
	ErrTransactionClosed = fmt.Errorf("transaction closed")
	ErrTransactionBusy   = fmt.Errorf("transaction busy")
)

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
	return t.waitReady(time.Time{})
}

// WaitReadyTimeout is WaitReady with a deadline: returns ErrTransactionBusy if
// the transaction is still active after d.
func (t *SipTransaction) WaitReadyTimeout(d time.Duration) (unlock func(), err error) {
	return t.waitReady(time.Now().Add(d))
}

func (t *SipTransaction) waitReady(deadline time.Time) (unlock func(), err error) {
	if !deadline.IsZero() {
		// Wake waiters at the deadline.
		timer := time.AfterFunc(time.Until(deadline), func() {
			t.mu.Lock()
			t.cond.Broadcast()
			t.mu.Unlock()
		})
		defer timer.Stop()
	}

	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil, ErrTransactionClosed
	}
	for t.active {
		if !deadline.IsZero() && !time.Now().Before(deadline) {
			t.mu.Unlock()
			return nil, ErrTransactionBusy
		}
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
