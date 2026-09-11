package livekitbin

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNextLayout(t *testing.T) {
	all := []string{"a", "b", "c", "d", "e", "f", "g", "h"}

	t.Run("fills deterministically", func(t *testing.T) {
		require.Equal(t, []string{"a", "b", "c", "d", "e", "f"}, nextLayout(nil, nil, all, 6))
		require.Equal(t, nextLayout(nil, nil, all, 6), nextLayout(nil, nil, all, 6))
	})
	t.Run("keeps previous members and puts speakers first", func(t *testing.T) {
		prev := []string{"a", "b", "c", "d", "e", "f"}
		got := nextLayout(prev, []string{"g"}, all, 6)
		require.Equal(t, []string{"g", "a", "b", "c", "d", "e"}, got, "the newcomer displaces the tail, everyone else stays")
		got = nextLayout(got, []string{"c"}, all, 6)
		require.Equal(t, []string{"c", "g", "a", "b", "d", "e"}, got, "a member speaking changes order, not membership")
	})
	t.Run("drops absent participants and refills", func(t *testing.T) {
		prev := []string{"a", "b", "c", "d", "e", "f"}
		presentNow := []string{"a", "c", "e", "g", "h"}
		require.Equal(t, []string{"a", "c", "e", "g", "h"}, nextLayout(prev, nil, presentNow, 6))
	})
	t.Run("ignores unknown speakers and duplicates", func(t *testing.T) {
		require.Equal(t, []string{"b", "a"}, nextLayout(nil, []string{"b", "zz", "b"}, []string{"a", "b"}, 6))
	})
	t.Run("respects max", func(t *testing.T) {
		require.Len(t, nextLayout(nil, []string{"a", "b", "c"}, all, 2), 2)
		require.Nil(t, nextLayout(nil, nil, all, 0))
	})
}
