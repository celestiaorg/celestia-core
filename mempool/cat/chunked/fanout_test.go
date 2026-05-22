package chunked

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAnnounceFanout(t *testing.T) {
	cases := []struct {
		numParts uint32
		target   int
		want     int
	}{
		// Small txs: graduated formula raises fanout above the floor.
		{1, 60, 60},
		{2, 60, 30},
		{4, 60, 15},
		// At and past the floor crossover, fanout = MinAnnounceFanout.
		// ceil(60/8) = 8 < 15, so the floor wins.
		{8, 60, MinAnnounceFanout},
		{15, 60, MinAnnounceFanout},
		{30, 60, MinAnnounceFanout},
		{60, 60, MinAnnounceFanout},
		{120, 60, MinAnnounceFanout},
		{1024, 60, MinAnnounceFanout},
		// Default-target path stays consistent.
		{4, 0, AnnounceFanout(4, DefaultAnnounceTarget)},
	}
	for _, c := range cases {
		require.Equalf(t, c.want, AnnounceFanout(c.numParts, c.target),
			"numParts=%d target=%d", c.numParts, c.target)
	}
}

func TestPerPeerInflightCap(t *testing.T) {
	// 100 peers, 1024 chunks: half = 512, denom = ceil(100 * 0.33) = 33,
	// cap = ceil(512/33) = 16.
	require.Equal(t, 16, PerPeerInflightCap(1024, 100))

	// 4 peers (small cluster), 8 chunks: half = 4, denom = ceil(4 * 0.33) = 2,
	// cap = ceil(4/2) = 2.
	require.Equal(t, 2, PerPeerInflightCap(8, 4))

	// 1 peer: cap collapses to half.
	require.Equal(t, 4, PerPeerInflightCap(8, 1))

	// numParts == 0 returns 1 (avoid divide-by-zero in callers).
	require.Equal(t, 1, PerPeerInflightCap(0, 10))
}
