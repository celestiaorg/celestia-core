package types

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/tendermint/tendermint/crypto/merkle"
	tmbytes "github.com/tendermint/tendermint/libs/bytes"
)

func TestRowProofValidate(t *testing.T) {
	type testCase struct {
		name    string
		rp      RowProof
		root    []byte
		wantErr bool
	}
	testCases := []testCase{
		{
			name:    "empty row proof returns error",
			rp:      RowProof{},
			root:    root,
			wantErr: true,
		},
		{
			name:    "row proof with mismatched number of rows and row roots returns error",
			rp:      mismatchedRowRoots(),
			root:    root,
			wantErr: true,
		},
		{
			name:    "row proof with mismatched number of proofs returns error",
			rp:      mismatchedProofs(),
			root:    root,
			wantErr: true,
		},
		{
			name:    "row proof with mismatched number of rows returns error",
			rp:      mismatchedRows(),
			root:    root,
			wantErr: true,
		},
		{
			name:    "valid row proof returns no error",
			rp:      validRowProof(),
			root:    root,
			wantErr: false,
		},
		{
			name:    "valid row proof with incorrect root returns error",
			rp:      validRowProof(),
			root:    incorrectRoot,
			wantErr: true,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.rp.Validate(tc.root)
			if tc.wantErr {
				assert.Error(t, got)
				return
			}
			assert.NoError(t, got)
		})
	}
}

// root is the root hash of the Merkle tree used in validRowProof
var root = []uint8{253, 198, 13, 57, 11, 53, 213, 208, 65, 121, 189, 176, 31, 118, 252, 172, 25, 222, 154, 99, 108, 7, 73, 79, 246, 24, 250, 89, 1, 210, 104, 214}

var incorrectRoot = bytes.Repeat([]byte{0}, 32)

// validRowProof returns a row proof for one row. This test data copied from
// ceelestia-app's pkg/proof/proof_test.go TestNewShareInclusionProof: "1
// transaction share"
func validRowProof() RowProof {
	return RowProof{
		RowRoots: tmbytes.FromBytes([]byte{0, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 1, 150, 209, 104, 210, 166, 232, 52, 233, 139, 33, 35, 86, 68, 242, 51, 53, 189, 86, 97, 80, 75, 230, 63, 14, 38, 99, 181, 144, 157, 207, 40, 38}),
		Proofs: []*merkle.Proof{
			{
				Total:    128,
				Index:    0,
				LeafHash: []byte{165, 234, 78, 111, 123, 248, 236, 205, 70, 225, 187, 193, 13, 229, 129, 25, 130, 150, 28, 128, 192, 1, 47, 187, 46, 138, 68, 9, 34, 45, 138, 91},
				Aunts: [][]byte{
					{148, 77, 217, 119, 87, 96, 255, 160, 85, 105, 241, 119, 211, 37, 26, 186, 198, 102, 71, 115, 150, 32, 187, 16, 15, 215, 133, 64, 136, 218, 73, 213},
					{163, 69, 102, 180, 70, 79, 192, 226, 152, 60, 190, 159, 65, 94, 16, 36, 46, 161, 36, 117, 138, 224, 217, 52, 27, 72, 119, 218, 115, 98, 128, 216},
					{190, 92, 43, 9, 119, 63, 198, 68, 132, 202, 246, 241, 94, 252, 166, 19, 150, 112, 81, 29, 107, 180, 94, 57, 167, 243, 195, 142, 84, 181, 23, 222},
					{107, 180, 58, 88, 158, 87, 109, 242, 90, 204, 32, 93, 43, 162, 203, 159, 225, 11, 24, 11, 210, 135, 226, 191, 195, 169, 200, 25, 16, 117, 55, 152},
					{81, 254, 150, 120, 106, 67, 75, 41, 235, 91, 140, 136, 181, 165, 246, 33, 107, 216, 116, 221, 79, 121, 126, 249, 91, 109, 28, 235, 181, 191, 124, 85},
					{138, 2, 248, 240, 44, 125, 143, 105, 25, 184, 245, 117, 209, 43, 85, 28, 5, 154, 222, 156, 198, 14, 206, 29, 236, 198, 113, 12, 32, 58, 172, 87},
					{172, 2, 42, 181, 13, 193, 196, 198, 174, 221, 145, 152, 109, 159, 147, 126, 102, 255, 163, 107, 102, 96, 140, 116, 18, 202, 227, 4, 134, 86, 150, 158},
				},
			},
		},
		StartRow: 0,
		EndRow:   0,
	}
}

func mismatchedRowRoots() RowProof {
	rp := validRowProof()
	rp.RowRoots = []tmbytes.HexBytes{}
	return rp
}

func mismatchedProofs() RowProof {
	rp := validRowProof()
	rp.Proofs = []*merkle.Proof{}
	return rp
}

func mismatchedRows() RowProof {
	rp := validRowProof()
	rp.EndRow = 10
	return rp
}
