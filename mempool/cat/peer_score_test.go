package cat

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPeerScoreRanking(t *testing.T) {
	tbl := newPeerScoreTable(30 * time.Second)

	// peer 1: solid performer; peer 2: served an invalid chunk; peer 3: unknown.
	tbl.RecordChunkSuccess(1, 50*time.Millisecond, 256*1024)
	tbl.RecordChunkSuccess(1, 50*time.Millisecond, 256*1024)
	tbl.RecordReconstruction(1)

	tbl.RecordChunkSuccess(2, 50*time.Millisecond, 256*1024)
	tbl.RecordInvalidChunk(2)

	require.Greater(t, tbl.Score(1), 0.0)
	require.Less(t, tbl.Score(2), 0.0)
	require.Equal(t, 0.0, tbl.Score(3))

	ranked := tbl.Rank([]uint16{2, 3, 1})
	require.Equal(t, []uint16{1, 3, 2}, ranked)
}

func TestPeerScoreTimeoutLighterThanInvalid(t *testing.T) {
	tbl := newPeerScoreTable(30 * time.Second)
	tbl.RecordChunkTimeout(1)
	tbl.RecordInvalidChunk(2)
	// A single invalid chunk must hurt far more than a single timeout.
	require.Greater(t, tbl.Score(1), tbl.Score(2))
}

func TestPeerScoreDecay(t *testing.T) {
	tbl := newPeerScoreTable(10 * time.Second)
	base := time.Now()
	cur := base
	tbl.now = func() time.Time { return cur }

	tbl.RecordChunkSuccess(1, 0, 0) // goodChunks = 1
	before := tbl.Score(1)
	require.InDelta(t, 1.0, before, 1e-9)

	// Advance one halflife; the accumulator should roughly halve.
	cur = base.Add(10 * time.Second)
	after := tbl.Score(1)
	require.InDelta(t, 0.5, after, 1e-6)
}

func TestPeerScoreRemovePeer(t *testing.T) {
	tbl := newPeerScoreTable(30 * time.Second)
	tbl.RecordReconstruction(1)
	require.Greater(t, tbl.Score(1), 0.0)
	tbl.RemovePeer(1)
	require.Equal(t, 0.0, tbl.Score(1))
}

func TestManifestStoreFIFOEviction(t *testing.T) {
	ms := newManifestStore(2)

	keys := make([][32]byte, 3)
	for i := range keys {
		keys[i][0] = byte(i + 1)
	}
	ms.set(keys[0], nil)
	ms.set(keys[1], nil)
	require.Equal(t, 2, ms.len())
	require.True(t, ms.has(keys[0]))

	// Third insert evicts the oldest (keys[0]).
	ms.set(keys[2], nil)
	require.Equal(t, 2, ms.len())
	require.False(t, ms.has(keys[0]))
	require.True(t, ms.has(keys[1]))
	require.True(t, ms.has(keys[2]))

	ms.remove(keys[1])
	require.False(t, ms.has(keys[1]))
	require.Equal(t, 1, ms.len())
}
