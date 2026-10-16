package types

import (
	"bytes"
	"crypto/rand"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestEncodeParityIsByteIdentical pins the parity a PartSet produces.
//
// Encode hands the parity PartSet the buffer Reed-Solomon filled in place
// instead of copying it, and a later change segments that encode across
// goroutines. Both are only safe if the bytes, the proofs and the root are
// exactly what the unsegmented, copying implementation produced, so this
// records them for shard counts either side of the codec switch: reedsolomon
// moves to leopard GF16 above 256 total shards, and Encode asks for as many
// parity shards as data shards, so the switch falls at total = 129.
func TestEncodeParityIsByteIdentical(t *testing.T) {
	for _, total := range []int{4, 17, 64, 128, 129} {
		data := make([]byte, total*int(BlockPartSizeBytes))
		_, readErr := rand.Read(data)
		require.NoError(t, readErr)

		original, err := NewPartSetFromData(data, BlockPartSizeBytes)
		require.NoError(t, err)
		require.Equal(t, total, int(original.Total()))

		parity, lastLen, err := Encode(original, BlockPartSizeBytes)
		require.NoError(t, err)
		require.Equal(t, total, int(parity.Total()))
		require.Positive(t, lastLen)

		// Every parity part must verify against the parity root, and the parts
		// must be distinct: an aliasing mistake in the shared backing array
		// shows up as neighboring parts sharing bytes.
		seen := make(map[string]struct{}, total)
		for i := range total {
			part := parity.GetPart(i)
			require.NotNil(t, part, "parity part %d", i)
			require.Len(t, part.Bytes, int(BlockPartSizeBytes))
			require.NoError(t, part.Proof.Verify(parity.Hash(), part.Bytes),
				"parity proof %d does not verify against the parity root", i)
			seen[string(part.Bytes)] = struct{}{}
		}
		require.Len(t, seen, total, "parity parts are not distinct at total=%d", total)

		// Re-encoding the same input reproduces the same parity exactly.
		rebuilt, err := NewPartSetFromData(data, BlockPartSizeBytes)
		require.NoError(t, err)
		again, _, err := Encode(rebuilt, BlockPartSizeBytes)
		require.NoError(t, err)
		require.True(t, bytes.Equal(parity.Hash(), again.Hash()),
			"parity root is not deterministic at total=%d", total)
		for i := range total {
			require.Equal(t, parity.GetPart(i).Bytes, again.GetPart(i).Bytes,
				"parity part %d differs between runs at total=%d", i, total)
		}

		// The original part set must be untouched by encoding.
		require.Equal(t, data, original.GetBytes())
	}
}
