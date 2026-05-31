package cat

import (
	"bytes"
	"crypto/rand"
	"testing"

	"github.com/stretchr/testify/require"

	protomem "github.com/cometbft/cometbft/proto/tendermint/mempool"
	"github.com/cometbft/cometbft/types"
)

func randBytes(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	_, err := rand.Read(b)
	require.NoError(t, err)
	return b
}

func TestNumChunks(t *testing.T) {
	require.Equal(t, 0, numChunks(0, 100))
	require.Equal(t, 1, numChunks(1, 100))
	require.Equal(t, 1, numChunks(100, 100))
	require.Equal(t, 2, numChunks(101, 100))
	require.Equal(t, 4, numChunks(400, 100))
	require.Equal(t, 5, numChunks(401, 100))
	require.Equal(t, 0, numChunks(100, 0))
}

func TestBuildManifestAndReconstruct(t *testing.T) {
	tx := randBytes(t, 3*minLargeTxChunkSize+123)
	key := types.Tx(tx).Key()
	chunkSize := minLargeTxChunkSize

	m := buildManifest(key, tx, chunkSize, []byte("signer"), 7, 42)
	require.Equal(t, key[:], m.TxKey)
	require.Equal(t, uint32(len(tx)), m.TxSize)
	require.Equal(t, uint32(chunkSize), m.ChunkSize)
	require.Equal(t, uint32(4), m.ChunkCount)
	require.Len(t, m.ChunkHashes, 4)
	require.Equal(t, uint64(7), m.Sequence)
	require.Equal(t, uint64(42), m.Priority)

	// Gather chunks via chunkData and verify each.
	chunks := make([][]byte, m.ChunkCount)
	for i := 0; i < int(m.ChunkCount); i++ {
		c, err := chunkData(m, tx, i)
		require.NoError(t, err)
		require.NoError(t, verifyChunk(m, i, c))
		// copy because chunkData returns a slice into tx
		chunks[i] = append([]byte(nil), c...)
	}

	got, err := reconstructTx(m, chunks)
	require.NoError(t, err)
	require.True(t, bytes.Equal(tx, got))
}

func TestReconstructDetectsTampering(t *testing.T) {
	tx := randBytes(t, 2*minLargeTxChunkSize)
	key := types.Tx(tx).Key()
	m := buildManifest(key, tx, minLargeTxChunkSize, nil, 0, 0)

	chunks := make([][]byte, m.ChunkCount)
	for i := range chunks {
		c, err := chunkData(m, tx, i)
		require.NoError(t, err)
		chunks[i] = append([]byte(nil), c...)
	}
	// Flip a byte in the first chunk; reconstruction must fail the tx_key check.
	chunks[0][0] ^= 0xff
	_, err := reconstructTx(m, chunks)
	require.ErrorIs(t, err, errReconstructBadTxKey)
}

func TestReconstructIncomplete(t *testing.T) {
	tx := randBytes(t, 2*minLargeTxChunkSize)
	key := types.Tx(tx).Key()
	m := buildManifest(key, tx, minLargeTxChunkSize, nil, 0, 0)

	// Missing chunk (nil).
	_, err := reconstructTx(m, make([][]byte, m.ChunkCount))
	require.ErrorIs(t, err, errReconstructIncomplete)

	// Wrong number of chunks.
	_, err = reconstructTx(m, [][]byte{{1}})
	require.ErrorIs(t, err, errReconstructIncomplete)
}

func TestVerifyChunk(t *testing.T) {
	tx := randBytes(t, 2*minLargeTxChunkSize+10)
	key := types.Tx(tx).Key()
	m := buildManifest(key, tx, minLargeTxChunkSize, nil, 0, 0)

	last := int(m.ChunkCount) - 1
	c, err := chunkData(m, tx, last)
	require.NoError(t, err)
	require.Equal(t, 10, len(c)) // final partial chunk
	require.NoError(t, verifyChunk(m, last, c))

	// Wrong size.
	require.ErrorIs(t, verifyChunk(m, last, append(c, 0)), errChunkWrongSize)
	// Corrupted data of correct size.
	bad := append([]byte(nil), c...)
	bad[0] ^= 0x01
	require.ErrorIs(t, verifyChunk(m, last, bad), errChunkHashMismatch)
	// Out of range.
	require.ErrorIs(t, verifyChunk(m, int(m.ChunkCount), c), errChunkIndexOutOfRange)
}

func TestValidateTxManifest(t *testing.T) {
	maxTxBytes := 8 * 1024 * 1024
	tx := randBytes(t, 2*minLargeTxChunkSize+5)
	key := types.Tx(tx).Key()
	good := buildManifest(key, tx, minLargeTxChunkSize, []byte("s"), 1, 1)
	require.NoError(t, validateTxManifest(good, maxTxBytes))

	clone := func() *protomem.TxManifest {
		c := *good
		c.ChunkHashes = append([][]byte(nil), good.ChunkHashes...)
		return &c
	}

	tests := []struct {
		name   string
		mutate func(*protomem.TxManifest)
		err    error
	}{
		{"bad tx_key", func(m *protomem.TxManifest) { m.TxKey = m.TxKey[:10] }, errManifestBadTxKey},
		{"zero tx_size", func(m *protomem.TxManifest) { m.TxSize = 0 }, errManifestBadTxSize},
		{"oversize tx_size", func(m *protomem.TxManifest) { m.TxSize = uint32(maxTxBytes + 1) }, errManifestBadTxSize},
		{"chunk too small", func(m *protomem.TxManifest) { m.ChunkSize = minLargeTxChunkSize - 1 }, errManifestBadChunkSize},
		{"chunk too big", func(m *protomem.TxManifest) { m.ChunkSize = maxLargeTxChunkSize + 1 }, errManifestBadChunkSize},
		{"count mismatch", func(m *protomem.TxManifest) { m.ChunkCount = good.ChunkCount + 1 }, errManifestBadChunkCount},
		{"hash count mismatch", func(m *protomem.TxManifest) { m.ChunkHashes = m.ChunkHashes[:1] }, errManifestBadHashCount},
		{"bad hash len", func(m *protomem.TxManifest) { m.ChunkHashes[0] = []byte{1, 2, 3} }, errManifestBadHashLen},
		{"signer too long", func(m *protomem.TxManifest) { m.Signer = make([]byte, maxSignerLength+1) }, errManifestSignerTooLong},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := clone()
			tc.mutate(m)
			require.ErrorIs(t, validateTxManifest(m, maxTxBytes), tc.err)
		})
	}
}
