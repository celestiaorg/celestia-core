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
			name: "Block with bad encoding",
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
		{
			name: "Incorrect number of shares",
			input: BadEncodingFraudProof{
				Height:      10,
				ShareProofs: []tmproto.ShareProof{},
				IsCol:       true,
				Position:    12,
			},
			output: false,
			err:    nil, // How do we denote the error type?
		},
		{
			name: "Position out of bound",
			input: BadEncodingFraudProof{
				Height:      10,
				ShareProofs: []tmproto.ShareProof{},
				IsCol:       true,
				Position:    12,
			},
			output: false,
			err:    nil,
		},
		{
			name: "Non committed shares",
			input: BadEncodingFraudProof{
				Height:      10,
				ShareProofs: []tmproto.ShareProof{},
				IsCol:       true,
				Position:    12,
			},
			output: false,
			err:    nil,
		},
		{
			name: "Default",
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
