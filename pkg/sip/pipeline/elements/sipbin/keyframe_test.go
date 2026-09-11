package sipbin

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestKeyframeDemandWindow(t *testing.T) {
	tr := &SipTrack{}
	now := time.Now()
	require.False(t, tr.keyframeDemanded(now), "no demand yet: the periodic FIR/PLI stays idle")

	tr.NoteKeyframeDemand()
	require.True(t, tr.keyframeDemanded(time.Now()))
	require.True(t, tr.keyframeDemanded(time.Now().Add(keyframeDemandWindow-time.Second)))
	require.False(t, tr.keyframeDemanded(time.Now().Add(keyframeDemandWindow+time.Second)),
		"the periodic request must stop once the decoder stops asking")
}
