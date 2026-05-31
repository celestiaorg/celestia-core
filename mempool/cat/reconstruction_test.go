package cat

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/mempool"
	"github.com/cometbft/cometbft/types"
)

func newTestSession(t *testing.T, txLen int) (*reconstructionSession, []byte) {
	t.Helper()
	tx := randBytes(t, txLen)
	key := types.Tx(tx).Key()
	m := buildManifest(key, tx, minLargeTxChunkSize, []byte("signer"), 1, 1)
	s := newReconstructionSession(m, mempool.TxInfo{}, time.Now(), DefaultLargeTxReconstructionTimeout)
	return s, tx
}

func TestSessionReconstructFromChunks(t *testing.T) {
	s, tx := newTestSession(t, 3*minLargeTxChunkSize+99)
	require.False(t, s.isComplete())

	for i := 0; i < int(s.manifest.ChunkCount); i++ {
		start, end := chunkBounds(len(tx), minLargeTxChunkSize, i)
		added, err := s.addChunk(i, tx[start:end], uint16(i+1))
		require.NoError(t, err)
		require.True(t, added)
	}
	require.True(t, s.isComplete())

	got, err := s.reconstruct()
	require.NoError(t, err)
	require.Equal(t, tx, got)
}

func TestSessionRejectsBadChunkAndDuplicates(t *testing.T) {
	s, tx := newTestSession(t, 2*minLargeTxChunkSize)

	// Corrupted chunk is rejected and not stored.
	bad := append([]byte(nil), tx[:minLargeTxChunkSize]...)
	bad[0] ^= 0xff
	added, err := s.addChunk(0, bad, 1)
	require.Error(t, err)
	require.False(t, added)
	require.ErrorIs(t, err, errChunkHashMismatch)

	// Valid chunk added once; duplicate returns false, no error.
	added, err = s.addChunk(0, tx[:minLargeTxChunkSize], 1)
	require.NoError(t, err)
	require.True(t, added)
	added, err = s.addChunk(0, tx[:minLargeTxChunkSize], 2)
	require.NoError(t, err)
	require.False(t, added)
}

func TestSessionInflightAndMissing(t *testing.T) {
	s, _ := newTestSession(t, 4*minLargeTxChunkSize)
	require.Equal(t, []int{0, 1, 2, 3}, s.missing())

	s.markInflight(5, 0)
	s.markInflight(5, 1)
	require.Equal(t, 2, s.inflightCountForPeer(5))
	require.True(t, s.isInflight(0))
	// inflight indexes are excluded from missing
	require.Equal(t, []int{2, 3}, s.missing())

	s.clearInflight(5, 0)
	require.Equal(t, 1, s.inflightCountForPeer(5))
	require.Equal(t, []int{0, 2, 3}, s.missing())
}

func TestSessionRemovePeerReturnsOrphans(t *testing.T) {
	s, _ := newTestSession(t, 3*minLargeTxChunkSize)
	s.addPeer(7)
	s.markInflight(7, 0)
	s.markInflight(7, 2)
	orphans := s.removePeer(7)
	require.Equal(t, []int{0, 2}, orphans)
	require.Equal(t, 0, s.inflightCountForPeer(7))
}

func TestReconstructionManager(t *testing.T) {
	rm := newReconstructionManager(DefaultLargeTxReconstructionTimeout)
	tx := randBytes(t, 2*minLargeTxChunkSize)
	key := types.Tx(tx).Key()
	m := buildManifest(key, tx, minLargeTxChunkSize, nil, 0, 0)

	s, created := rm.create(m, mempool.TxInfo{}, time.Now())
	require.True(t, created)
	require.True(t, rm.has(key))

	// Second create returns existing session, created=false.
	s2, created := rm.create(m, mempool.TxInfo{}, time.Now())
	require.False(t, created)
	require.Same(t, s, s2)

	rm.remove(key)
	require.False(t, rm.has(key))
	require.Equal(t, 0, rm.len())
}

func TestManagerRemovePeer(t *testing.T) {
	rm := newReconstructionManager(DefaultLargeTxReconstructionTimeout)
	tx := randBytes(t, 3*minLargeTxChunkSize)
	key := types.Tx(tx).Key()
	m := buildManifest(key, tx, minLargeTxChunkSize, nil, 0, 0)
	s, _ := rm.create(m, mempool.TxInfo{}, time.Now())
	s.addPeer(9)
	s.markInflight(9, 1)

	orphaned := rm.removePeer(9)
	require.Equal(t, []int{1}, orphaned[key])
}
