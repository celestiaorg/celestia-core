package types

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tendermint/tendermint/crypto"
	cmtrand "github.com/tendermint/tendermint/libs/rand"
	cmtproto "github.com/tendermint/tendermint/proto/tendermint/types"
)

// TestMarshalBlockWithTxPositions uses table-driven tests to check that
// MarshalBlockWithTxPositions returns correct tx positions for blocks
// with different numbers and sizes of txs.
func TestMarshalBlockWithTxPositions(t *testing.T) {
	tests := []struct {
		name    string
		txSizes []int
	}{
		{
			name:    "No txs",
			txSizes: []int{},
		},
		{
			name:    "Single tx",
			txSizes: []int{10},
		},
		{
			name:    "Multiple txs",
			txSizes: []int{5, 20, 15},
		},
		{
			name:    "Multiple large txs",
			txSizes: []int{64000, 1000, 1000000, 2000000, 8000000},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a block with the specified tx sizes.
			// (Assumes makeBlock creates a Block with Data containing repeated txs.)
			block := makeProtoBlock(t, tt.txSizes)

			// Marshal block and capture the tx positions.
			b, positions, err := MarshalBlockWithTxPositions(block)
			require.NoError(t, err)

			bzb, err := block.Marshal()
			require.NoError(t, err)
			assert.Equal(t, b, bzb, "MarshalBlockWithTxPositions output should match proto.Marshal output")

			// The number of tx positions should match the number of txs.
			assert.Equal(t, len(tt.txSizes), len(positions), "unexpected number of tx positions")

			// For each tx position, verify that the field is well formed.
			// Each tx field is expected to be encoded as:
			//   tag (varint) + length (varint) + raw tx bytes
			for i, pos := range positions {
				// Check that positions are within bounds.
				assert.GreaterOrEqual(t, pos.Start, uint32(0), "position start must be non-negative")
				assert.LessOrEqual(t, pos.End, uint32(len(b)), "position end must be within message bounds")

				fieldBytes := b[pos.Start:pos.End]

				// Read the tag (should correspond to field number 1, the txs field).
				tag, n, err := readVarint(fieldBytes)
				require.NoError(t, err)
				fieldNum := int(tag >> 3)
				assert.Equal(t, 1, fieldNum, "expected field number 1 for txs")

				// Read the length of the tx content.
				length, _, err := readVarint(fieldBytes[n:])
				require.NoError(t, err)
				assert.Equal(t, tt.txSizes[i], int(length), "tx length does not match expected size")
			}
		})
	}
}

func makeProtoBlock(t *testing.T, txs []int) *cmtproto.Block {
	bztxs := make([]Tx, len(txs))
	for i, tx := range txs {
		bztxs[i] = cmtrand.Bytes(tx)
	}
	h := cmtrand.Int63()
	c1 := randCommit(time.Now())
	evidenceTime := time.Date(2019, 1, 1, 0, 0, 0, 0, time.UTC)
	evi := NewMockDuplicateVoteEvidence(h, evidenceTime, "block-test-chain")
	b1 := MakeBlock(h, makeData(bztxs), c1, []Evidence{evi})
	b1.ProposerAddress = cmtrand.Bytes(crypto.AddressSize)
	pb, err := b1.ToProto()
	require.NoError(t, err)
	return pb
}
