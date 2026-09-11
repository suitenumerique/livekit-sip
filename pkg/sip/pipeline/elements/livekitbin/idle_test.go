package livekitbin

import (
	"testing"
	"time"

	"github.com/go-gst/go-gst/gst"
	"github.com/stretchr/testify/require"
)

func TestIdleBookkeeping(t *testing.T) {
	e := &LivekitBin{idleGrace: time.Hour}
	calls := 0
	unsub := func(*gst.Bin) error { calls++; return nil }

	require.False(t, e.cancelIdle("TR_1"), "nothing scheduled yet")
	e.markIdle("TR_1", unsub)
	e.markIdle("TR_1", unsub)
	require.Len(t, e.idle, 1, "a second markIdle must not reschedule")

	require.True(t, e.cancelIdle("TR_1"), "promotion cancels the pending unsubscription")
	require.Empty(t, e.idle)
	require.Zero(t, calls)

	e.markIdle("TR_2", unsub)
	e.markIdle("TR_3", unsub)
	e.stopIdleTimers()
	require.Nil(t, e.idle)
	_, ok := e.takeIdle("TR_2")
	require.False(t, ok)
	require.Zero(t, calls, "stopped timers never unsubscribe")
}
