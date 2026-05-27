package chunked

import (
	"bytes"
	"crypto/rand"
	"testing"

	"github.com/cometbft/cometbft/crypto/merkle"
	"github.com/stretchr/testify/require"
)

// TestProofsFromLeafHashesMatchesMerkle verifies that proofs generated
// from leaf hashes (no raw data) match what the merkle package generates
// from raw data, for several tree sizes. If this drifts the chunked-store
// partial-state serving will silently produce invalid proofs.
func TestProofsFromLeafHashesMatchesMerkle(t *testing.T) {
	for _, n := range []int{1, 2, 3, 4, 5, 7, 8, 16, 31, 32, 62, 128, 257} {
		t.Run(itoa(n), func(t *testing.T) {
			items := make([][]byte, n)
			for i := range items {
				items[i] = make([]byte, 64)
				_, _ = rand.Read(items[i])
			}
			refRoot, refProofs := merkle.ProofsFromByteSlices(items)

			// Extract leaf hashes from reference proofs.
			leafHashes := make([][]byte, n)
			for i, p := range refProofs {
				leafHashes[i] = p.LeafHash
			}

			gotRoot, gotProofs := proofsFromLeafHashes(leafHashes)
			require.True(t, bytes.Equal(refRoot, gotRoot), "root mismatch for n=%d", n)
			require.Equal(t, len(refProofs), len(gotProofs))

			for i := range refProofs {
				require.Equal(t, refProofs[i].Total, gotProofs[i].Total, "Total mismatch i=%d n=%d", i, n)
				require.Equal(t, refProofs[i].Index, gotProofs[i].Index, "Index mismatch i=%d n=%d", i, n)
				require.True(t, bytes.Equal(refProofs[i].LeafHash, gotProofs[i].LeafHash), "LeafHash mismatch i=%d n=%d", i, n)
				require.Equal(t, len(refProofs[i].Aunts), len(gotProofs[i].Aunts), "Aunts len mismatch i=%d n=%d", i, n)
				for k := range refProofs[i].Aunts {
					require.True(t, bytes.Equal(refProofs[i].Aunts[k], gotProofs[i].Aunts[k]),
						"Aunts[%d] mismatch i=%d n=%d", k, i, n)
				}
				// Cross-check: verify with raw data.
				require.NoError(t, gotProofs[i].Verify(gotRoot, items[i]),
					"Verify failed i=%d n=%d", i, n)
			}
		})
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
