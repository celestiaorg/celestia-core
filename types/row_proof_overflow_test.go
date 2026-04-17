package types

import (
	"crypto/rand"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRowProofUint32Overflow verifies that a RowProof with StartRow=0 and
// EndRow=MaxUint32 is rejected. Previously, uint32 arithmetic caused
// EndRow - StartRow + 1 to overflow to 0, which matched len(nil)==0 and
// allowed an empty proof to validate against any data root.
func TestRowProofUint32Overflow(t *testing.T) {
	rp := RowProof{
		RowRoots: nil,
		Proofs:   nil,
		StartRow: 0,
		EndRow:   math.MaxUint32,
	}

	for i := 0; i < 10; i++ {
		root := make([]byte, 32)
		_, err := rand.Read(root)
		require.NoError(t, err)

		err = rp.Validate(root)
		assert.Error(t, err, "empty RowProof with overflow should be rejected for root %x", root)
	}
}

// TestRowProofEmptyRejected confirms that an empty RowProof is always rejected,
// even without the uint32 overflow.
func TestRowProofEmptyRejected(t *testing.T) {
	rp := RowProof{
		RowRoots: nil,
		Proofs:   nil,
		StartRow: 0,
		EndRow:   0,
	}

	root := make([]byte, 32)
	_, err := rand.Read(root)
	require.NoError(t, err)

	err = rp.Validate(root)
	assert.Error(t, err, "empty RowProof should be rejected")
}

// TestShareProofUint32Overflow verifies that the uint32 overflow in RowProof
// does not cascade through ShareProof.Validate() to allow empty proofs.
func TestShareProofUint32Overflow(t *testing.T) {
	sp := ShareProof{
		Data:        nil,
		ShareProofs: nil,
		NamespaceID: nil,
		RowProof: RowProof{
			RowRoots: nil,
			Proofs:   nil,
			StartRow: 0,
			EndRow:   math.MaxUint32,
		},
		NamespaceVersion: 0,
	}

	for i := 0; i < 10; i++ {
		root := make([]byte, 32)
		_, err := rand.Read(root)
		require.NoError(t, err)

		err = sp.Validate(root)
		assert.Error(t, err, "empty ShareProof with overflow should be rejected for root %x", root)
	}
}
