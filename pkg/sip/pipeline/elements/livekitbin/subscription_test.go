package livekitbin

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSubscriptionRequests(t *testing.T) {
	e := &LivekitBin{}

	require.False(t, e.wantsTrack("TR_1"))
	require.False(t, e.dropTrack("TR_1"), "nothing to release")

	require.True(t, e.wantTrack("TR_1"))
	require.False(t, e.wantTrack("TR_1"), "a pending request is not repeated")
	require.True(t, e.wantsTrack("TR_1"))

	require.True(t, e.dropTrack("TR_1"))
	require.False(t, e.dropTrack("TR_1"), "a release is reported once")
	require.False(t, e.wantsTrack("TR_1"))

	require.True(t, e.wantTrack("TR_1"), "a released track can be requested again")
	require.True(t, e.wantTrack("TR_2"))
	require.Len(t, e.wanted, 2)
}
