package types

import (
	"testing"

	"github.com/stretchr/testify/assert"

	types "github.com/cometbft/cometbft/proto/tendermint/types"
)

func TestShareProofValidate(t *testing.T) {
	type testCase struct {
		name    string
		sp      ShareProof
		root    []byte
		wantErr bool
	}

	testCases := []testCase{
		{
			name:    "empty share proof returns error",
			sp:      ShareProof{},
			root:    shareProofRoot,
			wantErr: true,
		},
		{
			name:    "valid share proof returns no error",
			sp:      validShareProof(),
			root:    shareProofRoot,
			wantErr: false,
		},
		{
			name:    "share proof with mismatched number of share proofs returns error",
			sp:      mismatchedShareProofs(),
			root:    shareProofRoot,
			wantErr: true,
		},
		{
			name:    "share proof with mismatched number of shares returns error",
			sp:      mismatchedShares(),
			root:    shareProofRoot,
			wantErr: true,
		},
		{
			name:    "valid share proof with incorrect root returns error",
			sp:      validShareProof(),
			root:    incorrectRoot,
			wantErr: true,
		},
		{
			name:    "share proof with nil proof entry returns error",
			sp:      nilShareProofEntry(),
			root:    shareProofRoot,
			wantErr: true,
		},
		{
			name:    "share proof with negative start returns error",
			sp:      negativeStartShareProof(),
			root:    shareProofRoot,
			wantErr: true,
		},
		{
			name:    "share proof with empty range returns error",
			sp:      emptyRangeShareProof(),
			root:    shareProofRoot,
			wantErr: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.sp.Validate(tc.root)
			if tc.wantErr {
				assert.Error(t, got)
				return
			}
			assert.NoError(t, got)
		})
	}
}

func mismatchedShareProofs() ShareProof {
	sp := validShareProof()
	sp.ShareProofs = []*types.NMTProof{}
	return sp
}

func nilShareProofEntry() ShareProof {
	sp := validShareProof()
	sp.ShareProofs[0] = nil
	return sp
}

func negativeStartShareProof() ShareProof {
	sp := validShareProof()
	sp.ShareProofs[0].Start = -1
	return sp
}

func emptyRangeShareProof() ShareProof {
	sp := validShareProof()
	sp.ShareProofs[0].End = sp.ShareProofs[0].Start
	return sp
}

func mismatchedShares() ShareProof {
	sp := validShareProof()
	sp.Data = [][]byte{}
	return sp
}

// validShareProof returns a valid ShareProof for two shares in one row of the
// test square. shareProofRoot is the data root it is valid for.
func validShareProof() ShareProof {
	return newTestSquare().shareProof(testNamespace, rowRange{0, 0, 2})
}

var shareProofRoot = newTestSquare().root
