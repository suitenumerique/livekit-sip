package livekitbin

import (
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The SSRC lookup is written from the GLib main loop (subscribe/unsubscribe)
// and read from rtpbin streaming threads (pad-added): it must stay consistent
// under -race with both sides interleaving.
func TestTrackBookkeepingConcurrent(t *testing.T) {
	e := &LivekitBin{
		tracks:    make(map[string]*LivekitBinTrack),
		sidBySsrc: make(map[uint32]string),
	}
	const workers, rounds = 32, 200

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				for i := 0; i < workers; i++ {
					if sid, tr, ok := e.trackBySSRC(uint32(1000 + i)); ok {
						assert.Equal(t, fmt.Sprintf("TR_%d", i), sid)
						assert.NotNil(t, tr)
					}
				}
			}
		}()
	}

	var writers sync.WaitGroup
	for i := 0; i < workers; i++ {
		writers.Add(1)
		go func(i int) {
			defer writers.Done()
			sid, ssrc := fmt.Sprintf("TR_%d", i), uint32(1000+i)
			for n := 0; n < rounds; n++ {
				tr := &LivekitBinTrack{}
				assert.True(t, e.registerTrack(sid, ssrc, tr))
				gotSid, got, ok := e.trackBySSRC(ssrc)
				assert.True(t, ok)
				assert.Equal(t, sid, gotSid)
				assert.Same(t, tr, got)
				lt, ok := e.lookupTrack(sid)
				assert.True(t, ok)
				assert.Same(t, tr, lt)
				_, ok = e.unregisterTrack(sid, ssrc)
				assert.True(t, ok)
				_, _, ok = e.trackBySSRC(ssrc)
				assert.False(t, ok)
			}
		}(i)
	}
	writers.Wait()
	close(stop)
	wg.Wait()

	require.Empty(t, e.tracks)
	require.Empty(t, e.sidBySsrc)
}

func TestTrackBookkeepingAfterClose(t *testing.T) {
	e := &LivekitBin{} // maps are nil once the bin is closed
	require.False(t, e.registerTrack("TR_1", 1, &LivekitBinTrack{}))
	_, _, ok := e.trackBySSRC(1)
	require.False(t, ok)
	_, ok = e.lookupTrack("TR_1")
	require.False(t, ok)
	_, ok = e.unregisterTrack("TR_1", 1)
	require.False(t, ok)
}

func TestTrackBookkeepingUnregisterUnknown(t *testing.T) {
	e := &LivekitBin{
		tracks:    make(map[string]*LivekitBinTrack),
		sidBySsrc: make(map[uint32]string),
	}
	require.True(t, e.registerTrack("TR_1", 1, &LivekitBinTrack{}))
	_, ok := e.unregisterTrack("TR_2", 2)
	require.False(t, ok, "unknown SID must not disturb existing entries")
	_, _, ok = e.trackBySSRC(1)
	require.True(t, ok)
	_, ok = e.unregisterTrack("TR_1", 1)
	require.True(t, ok)
	_, ok = e.unregisterTrack("TR_1", 1)
	require.False(t, ok, "second unregister is a no-op")
}
