package chunked

import (
	"bytes"
	"crypto/rand"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEncodeSingleChunk(t *testing.T) {
	body := make([]byte, 1024)
	_, err := rand.Read(body)
	require.NoError(t, err)

	enc, err := Encode(body)
	require.NoError(t, err)
	require.Equal(t, uint32(1), enc.NumParts())
	require.Equal(t, uint32(1), enc.NumOriginals())
	require.Equal(t, uint32(1024), enc.LastLength)
	require.Empty(t, enc.Parity)
	require.Nil(t, enc.Proofs)

	require.NoError(t, VerifyChunk(enc.PartsRoot, enc.NumParts(), 0, enc.Originals[0], nil))

	out, err := Decode([][]byte{enc.Originals[0]}, enc.LastLength)
	require.NoError(t, err)
	require.True(t, bytes.Equal(body, out))
}

func TestEncodeMultiChunkRoundtrip(t *testing.T) {
	for _, bodyLen := range []int{
		ChunkSize + 1,
		3*ChunkSize - 100,
		16 * ChunkSize,
	} {
		body := make([]byte, bodyLen)
		_, err := rand.Read(body)
		require.NoError(t, err)

		enc, err := Encode(body)
		require.NoError(t, err)
		k := (bodyLen + ChunkSize - 1) / ChunkSize
		require.Equal(t, uint32(2*k), enc.NumParts())
		require.Equal(t, uint32(k), enc.NumOriginals())
		require.Len(t, enc.Proofs, 2*k)

		all := make([][]byte, 2*k)
		for i := 0; i < k; i++ {
			all[i] = enc.Originals[i]
		}
		for i := 0; i < k; i++ {
			all[k+i] = enc.Parity[i]
		}

		out, err := Decode(all, enc.LastLength)
		require.NoError(t, err)
		require.True(t, bytes.Equal(body, out))
	}
}

func TestDecodeFromKOfTwoK(t *testing.T) {
	body := make([]byte, 8*ChunkSize-37)
	_, err := rand.Read(body)
	require.NoError(t, err)
	enc, err := Encode(body)
	require.NoError(t, err)
	k := int(enc.NumOriginals())

	// Drop all originals, keep only the K parity chunks.
	chunks := make([][]byte, 2*k)
	for i := k; i < 2*k; i++ {
		chunks[i] = enc.Parity[i-k]
	}
	out, err := Decode(chunks, enc.LastLength)
	require.NoError(t, err)
	require.True(t, bytes.Equal(body, out))

	// Drop K of the 2K chunks (any half); decode still works.
	chunks2 := make([][]byte, 2*k)
	for i := 0; i < 2*k; i++ {
		if i%2 == 0 {
			if i < k {
				chunks2[i] = enc.Originals[i]
			} else {
				chunks2[i] = enc.Parity[i-k]
			}
		}
	}
	out2, err := Decode(chunks2, enc.LastLength)
	require.NoError(t, err)
	require.True(t, bytes.Equal(body, out2))
}

func TestDecodeInsufficientChunks(t *testing.T) {
	body := make([]byte, 4*ChunkSize)
	_, err := rand.Read(body)
	require.NoError(t, err)
	enc, err := Encode(body)
	require.NoError(t, err)
	k := int(enc.NumOriginals())

	// Provide K-1 chunks: should fail.
	chunks := make([][]byte, 2*k)
	for i := 0; i < k-1; i++ {
		chunks[i] = enc.Originals[i]
	}
	_, err = Decode(chunks, enc.LastLength)
	require.ErrorIs(t, err, ErrInsufficientChunks)
}

func TestVerifyChunkMulti(t *testing.T) {
	body := make([]byte, 2*ChunkSize+10)
	_, err := rand.Read(body)
	require.NoError(t, err)
	enc, err := Encode(body)
	require.NoError(t, err)

	for i := uint32(0); i < enc.NumParts(); i++ {
		require.NoError(t, VerifyChunk(enc.PartsRoot, enc.NumParts(), i, enc.Chunk(i), enc.Proofs[i]))
	}

	// Tamper with a chunk: verification fails.
	tampered := append([]byte(nil), enc.Originals[0]...)
	tampered[0] ^= 0xff
	require.Error(t, VerifyChunk(enc.PartsRoot, enc.NumParts(), 0, tampered, enc.Proofs[0]))
}

func TestVerifyChunkSingleHashMismatch(t *testing.T) {
	body := []byte("hello world")
	enc, err := Encode(body)
	require.NoError(t, err)
	require.NoError(t, VerifyChunk(enc.PartsRoot, 1, 0, body, nil))
	require.ErrorIs(t, VerifyChunk(enc.PartsRoot, 1, 0, []byte("hello mars!"), nil), ErrSingleChunkProof)
}
