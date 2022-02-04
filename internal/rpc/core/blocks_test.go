package core

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/tendermint/tendermint/crypto/merkle"
	tmrand "github.com/tendermint/tendermint/libs/rand"
	"github.com/tendermint/tendermint/types"
	dbm "github.com/tendermint/tm-db"

	abci "github.com/tendermint/tendermint/abci/types"
	sm "github.com/tendermint/tendermint/internal/state"
	"github.com/tendermint/tendermint/internal/state/indexer"
	"github.com/tendermint/tendermint/internal/state/mocks"
	tmstate "github.com/tendermint/tendermint/proto/tendermint/state"
	"github.com/tendermint/tendermint/rpc/coretypes"
	rpctypes "github.com/tendermint/tendermint/rpc/jsonrpc/types"
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
			{Code: 0, Data: []byte{0x01}, Log: "ok", GasUsed: 10},
			{Code: 0, Data: []byte{0x02}, Log: "ok", GasUsed: 5},
			{Code: 1, Log: "not ok", GasUsed: 0},
		},
		EndBlock:   &abci.ResponseEndBlock{},
		BeginBlock: &abci.ResponseBeginBlock{},
	}

	env := &Environment{}
	env.StateStore = sm.NewStore(dbm.NewMemDB())
	err := env.StateStore.SaveABCIResponses(100, results)
	require.NoError(t, err)
	mockstore := &mocks.BlockStore{}
	mockstore.On("Height").Return(int64(100))
	mockstore.On("Base").Return(int64(1))
	env.BlockStore = mockstore

	testCases := []struct {
		height  int64
		wantErr bool
		wantRes *coretypes.ResultBlockResults
	}{
		{-1, true, nil},
		{0, true, nil},
		{101, true, nil},
		{100, false, &coretypes.ResultBlockResults{
			Height:                100,
			TxsResults:            results.DeliverTxs,
			TotalGasUsed:          15,
			BeginBlockEvents:      results.BeginBlock.Events,
			EndBlockEvents:        results.EndBlock.Events,
			ValidatorUpdates:      results.EndBlock.ValidatorUpdates,
			ConsensusParamUpdates: results.EndBlock.ConsensusParamUpdates,
		}},
	}

	for _, tc := range testCases {
		res, err := env.BlockResults(&rpctypes.Context{}, &tc.height)
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
	heights := []int64{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}

	blocks := randomBlocks(int64(len(heights)))
	mockstore := &mocks.BlockStore{}
	for i, height := range heights {
		mockstore.On("LoadBlock", height).Return(blocks[i])
		mockstore.On("LoadBlockMeta", height).Return(types.NewBlockMeta(blocks[i], nil))
	}

	mockEVS := mocks.EventSink{}
	mockEVS.On("SearchBlockEvents", mock.Anything, mock.Anything).Return(heights[1:3], nil)
	mockEVS.On("Type").Return(indexer.KV)

	env.EventSinks = append(env.EventSinks, &mockEVS)
	env.BlockStore = mockstore

	testCases := []struct {
		beginQuery int
		endQuery   int
		expectPass bool
	}{
		{1, 2, true},
		// {10, 9, false}, // TODO: mock errors?
		// {0, 1000, false},
	}

	for _, tc := range testCases {
		mockedQuery := fmt.Sprintf("block.height >= %d AND block.height <= %d", tc.beginQuery, tc.endQuery)

		actualCommitment, err := env.DataCommitment(&rpctypes.Context{}, mockedQuery)
		if tc.expectPass {
			require.Nil(t, err, "should generate the needed data commitment.")

			size := tc.endQuery - tc.beginQuery + 1
			dataRoots := make([][]byte, size)
			for i := 0; i < size; i++ {
				dataRoots[i] = blocks[tc.beginQuery+i].DataHash
			}
			expectedCommitment := merkle.HashFromByteSlices(dataRoots)

			if !bytes.Equal(expectedCommitment, actualCommitment.DataCommitment) {
				t.Error("expected data commitment and actual data commitment doesn't match.")
			}
		} else {
			assert.Error(t, err)
		}
	}
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
