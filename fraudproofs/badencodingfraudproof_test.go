package fraudproofs

import (
	"testing"

	"github.com/celestiaorg/celestia-core/types"
	tmproto "github.com/proto/tendermint/types"
	"github.com/stretchr/testify/require"
)

type BadEncodingError int

const (
	BadEncoding BadEncodingError = 0 + iota
	CorrectBlock
	Incorrect_number_of_shares
	Position_out_of_bound
	Non_committed_shares
)

func TestBadEncodingFraudProof(t *testing.T) {
	type test struct {
		name   string
		input  BadEncodingFraudProof
		dah    types.DataAvailabilityHeader
		output bool
		err    error
	}
	// TODO: template for table driven test for befp
	tests := []test{
		{
			name: "block with bad encoding",
			input: BadEncodingFraudProof{
				Height:      10,
				ShareProofs: []tmproto.ShareProof{},
				IsCol:       true,
				Position:    12,
			},
			output: true,
			err:    nil,
		},
		{
			name: "BadEncodingFraudProof for a correct block",
			input: BadEncodingFraudProof{
				Height:      10,
				ShareProofs: []tmproto.ShareProof{},
				IsCol:       true,
				Position:    12,
			},
			output: false,
			err:    nil,
		},
	}

	for _, tt := range tests {
		res, err := VerifyBadEncodingFraudProof(tt.input, tt.dah)
		require.Equal(t, tt.output, res)
		require.Equal(t, tt.err, err)
	}
}

// TODO: don't export and
// func CreateBadEncodingFraudProofWithError(block tmproto.Block, err int) tmproto.BadEncodingFraudProof {

// 	proof := CreateBadEncodingFraudProof(block)

// 	switch err {
// 	case Bad_encoding:
// 		// BadEncodingFraudProof for a block with bad encoding

// 	case Correct_block:
// 		// BadEncodingFraudProof for a correct block

// 	case Incorrect_number_of_shares:
// 		// BadEncodingFraudProof with insufficient or too many shares
// 		proof.Get

// 	case Position_out_of_bound:
// 		// BadEncodingFraudProof with position out of bound

// 	case Non_committed_shares:
// 		// BadEncodingFraudProof with shares such that the calculate root does not commit to the shares

// 	default:
// 		return CreateBadEncodingFraudProof(block)
// 	}

// 	return tmproto.bad
// }
