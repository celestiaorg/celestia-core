package core

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tendermint/tendermint/crypto/merkle"
	"github.com/tendermint/tendermint/libs/pubsub/query"
	tmrand "github.com/tendermint/tendermint/libs/rand"
	dbm "github.com/tendermint/tm-db"

	abci "github.com/tendermint/tendermint/abci/types"
	tmstate "github.com/tendermint/tendermint/proto/tendermint/state"
	ctypes "github.com/tendermint/tendermint/rpc/core/types"
	rpctypes "github.com/tendermint/tendermint/rpc/jsonrpc/types"
	sm "github.com/tendermint/tendermint/state"
	"github.com/tendermint/tendermint/types"
)

func TestBlockchainInfo(t *testing.T) {
	cases := []struct {
		min, max     int64
		base, height int64
		limit        int64
		resultLength int64
		wantErr      bool
	}{

		// min > max
		{0, 0, 0, 0, 10, 0, true},  // min set to 1
		{0, 1, 0, 0, 10, 0, true},  // max set to height (0)
		{0, 0, 0, 1, 10, 1, false}, // max set to height (1)
		{2, 0, 0, 1, 10, 0, true},  // max set to height (1)
		{2, 1, 0, 5, 10, 0, true},

		// negative
		{1, 10, 0, 14, 10, 10, false}, // control
		{-1, 10, 0, 14, 10, 0, true},
		{1, -10, 0, 14, 10, 0, true},
		{-9223372036854775808, -9223372036854775788, 0, 100, 20, 0, true},

		// check base
		{1, 1, 1, 1, 1, 1, false},
		{2, 5, 3, 5, 5, 3, false},

		// check limit and height
		{1, 1, 0, 1, 10, 1, false},
		{1, 1, 0, 5, 10, 1, false},
		{2, 2, 0, 5, 10, 1, false},
		{1, 2, 0, 5, 10, 2, false},
		{1, 5, 0, 1, 10, 1, false},
		{1, 5, 0, 10, 10, 5, false},
		{1, 15, 0, 10, 10, 10, false},
		{1, 15, 0, 15, 10, 10, false},
		{1, 15, 0, 15, 20, 15, false},
		{1, 20, 0, 15, 20, 15, false},
		{1, 20, 0, 20, 20, 20, false},
	}

	for i, c := range cases {
		caseString := fmt.Sprintf("test %d failed", i)
		min, max, err := filterMinMax(c.base, c.height, c.min, c.max, c.limit)
		if c.wantErr {
			require.Error(t, err, caseString)
		} else {
			require.NoError(t, err, caseString)
			require.Equal(t, 1+max-min, c.resultLength, caseString)
		}
	}
}

func TestBlockResults(t *testing.T) {
	results := &tmstate.ABCIResponses{
		DeliverTxs: []*abci.ResponseDeliverTx{
			{Code: 0, Data: []byte{0x01}, Log: "ok"},
			{Code: 0, Data: []byte{0x02}, Log: "ok"},
			{Code: 1, Log: "not ok"},
		},
		EndBlock:   &abci.ResponseEndBlock{},
		BeginBlock: &abci.ResponseBeginBlock{},
	}

	env := &Environment{}
	env.StateStore = sm.NewStore(dbm.NewMemDB())
	err := env.StateStore.SaveABCIResponses(100, results)
	require.NoError(t, err)
	env.BlockStore = mockBlockStore{height: 100}
	SetEnvironment(env)

	testCases := []struct {
		height  int64
		wantErr bool
		wantRes *ctypes.ResultBlockResults
	}{
		{-1, true, nil},
		{0, true, nil},
		{101, true, nil},
		{100, false, &ctypes.ResultBlockResults{
			Height:                100,
			TxsResults:            results.DeliverTxs,
			BeginBlockEvents:      results.BeginBlock.Events,
			EndBlockEvents:        results.EndBlock.Events,
			ValidatorUpdates:      results.EndBlock.ValidatorUpdates,
			ConsensusParamUpdates: results.EndBlock.ConsensusParamUpdates,
		}},
	}

	for _, tc := range testCases {
		res, err := BlockResults(&rpctypes.Context{}, &tc.height)
		if tc.wantErr {
			assert.Error(t, err)
		} else {
			assert.NoError(t, err)
			assert.Equal(t, tc.wantRes, res)
		}
	}
}

func TestDataCommitmentResults(t *testing.T) {
	env := &Environment{}
	height := int64(2826)

	blocks := randomBlocks(height)
	blockStore := mockBlockStore{
		height: height,
		blocks: blocks,
	}
	env.BlockStore = blockStore

	testCases := []struct {
		beginQuery int
		endQuery   int
		expectPass bool
	}{
		{10, 15, true},
		{2727, 2828, false},
		{10, 9, false},
		{0, 1000, false},
		{10, 8, false},
	}

	for _, tc := range testCases {
		env.BlockIndexer = mockBlockIndexer{
			height:          height,
			beginQueryBlock: tc.beginQuery,
			endQueryBlock:   tc.endQuery,
		}
		SetEnvironment(env)

		actualCommitment, err := DataCommitment(&rpctypes.Context{}, uint64(tc.beginQuery), uint64(tc.endQuery))
		if tc.expectPass {
			require.Nil(t, err, "should generate the needed data commitment.")

			size := tc.endQuery - tc.beginQuery + 1
			dataRoots := make([][]byte, size)
			for i := 0; i < size; i++ {
				dataRoots[i] = blocks[tc.beginQuery+i].DataHash
			}
			expectedCommitment := merkle.HashFromByteSlices(dataRoots)

			if !bytes.Equal(expectedCommitment, actualCommitment.DataCommitment) {
				assert.Error(t, nil, "expected data commitment and actual data commitment doesn't match.")
			}
		} else {
			require.NotNil(t, err, "couldn't generate the needed data commitment.")
		}
	}
}

func TestDataRootInclusionProofResults(t *testing.T) {
	env := &Environment{}
	env.StateStore = sm.NewStore(dbm.NewMemDB())

	height := int64(2826)
	env.BlockStore = mockBlockStore{height: height}
	SetEnvironment(env)

	blocks := randomBlocks(height)
	blockStore := mockBlockStore{
		height: height,
		blocks: blocks,
	}
	env.BlockStore = blockStore

	testCases := []struct {
		height     int
		firstQuery int
		lastQuery  int
		expectPass bool
	}{
		{8, 10, 15, false},
		{10, 10, 15, true},
		{13, 10, 15, true},
		{15, 10, 15, true},
		{17, 10, 15, false},
	}

	for _, tc := range testCases {
		env.BlockIndexer = mockBlockIndexer{
			height:          height,
			beginQueryBlock: tc.firstQuery,
			endQueryBlock:   tc.lastQuery,
		}

		proof, err := DataRootInclusionProof(
			&rpctypes.Context{},
			int64(tc.height),
			uint64(tc.firstQuery),
			uint64(tc.lastQuery),
		)
		if tc.expectPass {
			require.Nil(t, err, "should generate block height data root inclusion proof.")

			size := tc.lastQuery - tc.firstQuery + 1
			dataRoots := make([][]byte, size)
			for i := 0; i < size; i++ {
				dataRoots[i] = blocks[tc.firstQuery+i].DataHash
			}
			commitment := merkle.HashFromByteSlices(dataRoots)

			err = proof.Proof.Verify(commitment, dataRoots[tc.height-tc.firstQuery])
			require.NoError(t, err)
		} else {
			require.NotNil(t, err, "shouldn't be able to generate proof.")
		}
	}
}

type mockBlockStore struct {
	height int64
	blocks []*types.Block
}

func (mockBlockStore) Base() int64                                       { return 1 }
func (store mockBlockStore) Height() int64                               { return store.height }
func (store mockBlockStore) Size() int64                                 { return store.height }
func (mockBlockStore) LoadBaseMeta() *types.BlockMeta                    { return nil }
func (mockBlockStore) LoadBlockByHash(hash []byte) *types.Block          { return nil }
func (mockBlockStore) LoadBlockPart(height int64, index int) *types.Part { return nil }
func (mockBlockStore) LoadBlockCommit(height int64) *types.Commit        { return nil }
func (mockBlockStore) LoadSeenCommit(height int64) *types.Commit         { return nil }
func (mockBlockStore) PruneBlocks(height int64) (uint64, error)          { return 0, nil }
func (mockBlockStore) SaveBlock(block *types.Block, blockParts *types.PartSet, seenCommit *types.Commit) {
}

func (store mockBlockStore) LoadBlockMeta(height int64) *types.BlockMeta {
	if height > store.height {
		return nil
	}
	block := store.blocks[height]
	return &types.BlockMeta{
		BlockID: types.BlockID{Hash: block.Hash(), PartSetHeader: block.MakePartSet(types.BlockPartSizeBytes).Header()},
		Header:  block.Header,
	}
}

func (store mockBlockStore) LoadBlock(height int64) *types.Block {
	if height > store.height {
		return nil
	}
	return store.blocks[height]
}

// mockBlockIndexer used to mock the set of indexed blocks and return a predefined one.
type mockBlockIndexer struct {
	height          int64
	beginQueryBlock int // used not to have to parse any query
	endQueryBlock   int // used not to have to parse any query
}

func (indexer mockBlockIndexer) Has(height int64) (bool, error)            { return true, nil }
func (indexer mockBlockIndexer) Index(types.EventDataNewBlockHeader) error { return nil }

// Search returns a list of block heights corresponding to the values of `indexer.endQueryBlock`
// and `indexer.beginQueryBlock`.
// Doesn't use the query parameter for anything.
func (indexer mockBlockIndexer) Search(ctx context.Context, _ *query.Query) ([]int64, error) {
	size := indexer.endQueryBlock - indexer.beginQueryBlock + 1
	results := make([]int64, size)
	for i := 0; i < size; i++ {
		results[i] = int64(indexer.beginQueryBlock + i)
	}
	return results, nil
}

// randomBlocks generates a set of random blocks up to the provided height.
func randomBlocks(height int64) []*types.Block {
	blocks := make([]*types.Block, height)
	for i := int64(0); i < height; i++ {
		blocks[i] = randomBlock(i)
	}
	return blocks
}

// randomBlock generates a Block with a certain height and random data hash.
func randomBlock(height int64) *types.Block {
	return &types.Block{
		Header: types.Header{
			Height:   height,
			DataHash: tmrand.Bytes(32),
		},
	}
}
