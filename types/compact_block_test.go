package types

import (
	"testing"
	"time"

	"github.com/gogo/protobuf/proto"
	"github.com/stretchr/testify/require"
	"github.com/tendermint/tendermint/crypto"
	"github.com/tendermint/tendermint/crypto/tmhash"
)

func TestCompactBlockParity(t *testing.T) {
	tx := Tx("tx")
	const height = 10
	timestamp := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	header := makeHeaderRandom()
	cs := CommitSig{
		BlockIDFlag:      BlockIDFlagNil,
		ValidatorAddress: crypto.AddressHash([]byte("validator_address")),
		Timestamp:        timestamp,
		Signature:        crypto.CRandBytes(MaxSignatureSize),
	}
	lastCommit := &Commit{
		Height: height,
		Round:  0,
		BlockID: BlockID{
			Hash: tmhash.Sum([]byte("blockID_hash")),
			PartSetHeader: PartSetHeader{
				Total: 100,
				Hash:  tmhash.Sum([]byte("blockID_part_set_header_hash")),
			},
		},
		Signatures: []CommitSig{cs},
	}
	ev := NewMockDuplicateVoteEvidence(height, timestamp, "mock-chain-id")
	fullBlock := &Block{
		Header: *header,
		Data:   Data{Txs: []Tx{tx}},
		Evidence: EvidenceData{
			Evidence: []Evidence{ev},
		},
		LastCommit: lastCommit,
	}

	emptyBlock := &Block{
		Header:     *header,
		Data:       Data{Txs: []Tx{}},
		Evidence:   EvidenceData{},
		LastCommit: nil,
	}

	testCases := []struct {
		name  string
		block *Block
	}{
		{
			"full block with a single transaction",
			fullBlock,
		},
		{
			"empty block with no transactions and evidence",
			emptyBlock,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			keys := make([][]byte, len(tc.block.Data.Txs))
			for i, tx := range tc.block.Data.Txs {
				keys[i] = tx.Hash()
			}

			compactBlock, err := MakeCompactBlock(tc.block, keys)
			require.NoError(t, err)

			output, err := AssembleBlock(compactBlock, tc.block.Txs.ToSliceOfBytes())
			require.NoError(t, err)
			require.Equal(t, tc.block.Hash(), output.Hash())

			pbb, err := tc.block.ToProto()
			if err != nil {
				panic(err)
			}
			bzOriginal, err := proto.Marshal(pbb)
			if err != nil {
				panic(err)
			}
			pbb, err = output.ToProto()
			if err != nil {
				panic(err)
			}
			bzCopy, err := proto.Marshal(pbb)
			if err != nil {
				panic(err)
			}

			require.Equal(t, bzOriginal, bzCopy, tc.block.String(), output.String())
		})
	}

}
