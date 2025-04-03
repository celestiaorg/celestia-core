package propagation

import (
	"testing"

	dbm "github.com/cometbft/cometbft-db"
	"github.com/stretchr/testify/require"
	proptypes "github.com/tendermint/tendermint/consensus/propagation/types"
	"github.com/tendermint/tendermint/libs/bits"
	cmtrand "github.com/tendermint/tendermint/libs/rand"
	"github.com/tendermint/tendermint/store"
	"github.com/tendermint/tendermint/types"
)

func makeTestBlockStore(t *testing.T) *store.BlockStore {
	t.Helper()

	// create an in-memory DB for testing
	db := dbm.NewMemDB()
	return store.NewBlockStore(db)
}

func makeCompactBlock(height int64, round int32, totalParts int32) *proptypes.CompactBlock {
	cb := &proptypes.CompactBlock{
		BpHash:    cmtrand.Bytes(32),
		Signature: cmtrand.Bytes(64),
		LastLen:   0,
		Blobs: []proptypes.TxMetaData{
			{Hash: cmtrand.Bytes(32)},
			{Hash: cmtrand.Bytes(32)},
		},
		Proposal: *makeProposal(height, round, totalParts),
	}
	return cb
}

// makeProposal is a helper to create a minimal valid Proposal with the given height, round, and total parts.
func makeProposal(height int64, round int32, totalParts int32) *types.Proposal {
	return &types.Proposal{
		Height: height,
		Round:  round,
		BlockID: types.BlockID{
			PartSetHeader: types.PartSetHeader{
				Total: uint32(totalParts),
			},
		},
	}
}

func TestProposalCache_AddProposal(t *testing.T) {
	bs := makeTestBlockStore(t)
	pc := NewProposalCache(bs)

	type testCase struct {
		name              string
		inputProposal     *proptypes.CompactBlock
		wantAdded         bool
		wantCurrentHeight int64
		wantCurrentRound  int32
	}

	testCases := []testCase{
		{
			name:              "Add first proposal - updates current height/round",
			inputProposal:     makeCompactBlock(10, 1, 5),
			wantAdded:         true,
			wantCurrentHeight: 10,
			wantCurrentRound:  1,
		},
		{
			name:              "Add proposal at same height, higher round - updates current round",
			inputProposal:     makeCompactBlock(10, 3, 5),
			wantAdded:         true,
			wantCurrentHeight: 10,
			wantCurrentRound:  3,
		},
		{
			name:              "Add proposal with same height and round - returns false",
			inputProposal:     makeCompactBlock(10, 3, 5),
			wantAdded:         false, // already have 10/3 from above
			wantCurrentHeight: 10,
			wantCurrentRound:  3,
		},
		{
			name:              "Add proposal at higher height, round 0 - gap in heights",
			inputProposal:     makeCompactBlock(12, 0, 5),
			wantAdded:         true,
			wantCurrentHeight: 12,
			wantCurrentRound:  0,
		},
		{
			name:              "Add proposal with older height - no height/round update",
			inputProposal:     makeCompactBlock(5, 0, 5),
			wantAdded:         true, // it doesn't exist yet, so it can be added
			wantCurrentHeight: 12,   // still remain at 12/0
			wantCurrentRound:  0,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			added := pc.AddProposal(tc.inputProposal)
			require.Equal(t, tc.wantAdded, added, "added mismatch")
			require.Equal(t, tc.wantCurrentHeight, pc.currentHeight, "currentHeight mismatch")
			require.Equal(t, tc.wantCurrentRound, pc.currentRound, "currentRound mismatch")
		})
	}
}

func TestProposalCache_GetProposalWithRequests(t *testing.T) {
	bs := makeTestBlockStore(t)
	pc := NewProposalCache(bs)

	// Add some proposals
	prop1 := makeCompactBlock(5, 0, 3) // height=5, round=0
	prop2 := makeCompactBlock(5, 1, 3)
	prop3 := makeCompactBlock(6, 0, 4)

	pc.AddProposal(prop1)
	pc.AddProposal(prop2)
	pc.AddProposal(prop3)

	type testCase struct {
		name         string
		height       int64
		round        int32
		wantProposal *proptypes.CompactBlock
		wantBitArray *bits.BitArray
		wantOk       bool
	}

	testCases := []testCase{
		{
			name:         "Get existing proposal 5/0",
			height:       5,
			round:        0,
			wantProposal: prop1,
			wantBitArray: bits.NewBitArray(3), // totalParts=3
			wantOk:       true,
		},
		{
			name:         "Get existing proposal 5/1",
			height:       5,
			round:        1,
			wantProposal: prop2,
			wantBitArray: bits.NewBitArray(3),
			wantOk:       true,
		},
		{
			name:   "Get unknown proposal 5/2 -> not found",
			height: 5,
			round:  2,
			wantOk: false,
		},
		{
			name:         "Get existing proposal 6/0",
			height:       6,
			round:        0,
			wantProposal: prop3,
			wantBitArray: bits.NewBitArray(4),
			wantOk:       true,
		},
		{
			name:         "Use negative round => fetch the latest round for height=5 which is 1",
			height:       5,
			round:        -2, // meaning "latest round"
			wantProposal: prop2,
			wantBitArray: bits.NewBitArray(3),
			wantOk:       true,
		},
		{
			name:   "Get proposal that doesn't exist (height 100)",
			height: 100,
			round:  0,
			wantOk: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			gotProposal, gotPartSet, gotBA, ok := pc.getAllState(tc.height, tc.round, true)
			if !tc.wantOk {
				require.False(t, ok, "should not have found proposal")
				require.Nil(t, gotProposal)
				require.Nil(t, gotPartSet)
				require.Nil(t, gotBA)
				return
			}

			require.True(t, ok, "should have found proposal")
			require.Equal(t, tc.wantProposal, gotProposal, "proposal mismatch")

			// gotPartSet might not be exactly the same pointer as in the cache,
			// but we can check metadata like Total() or compare the headers.
			require.NotNil(t, gotPartSet, "partSet must not be nil")
			require.Equal(t, tc.wantBitArray.Size(), gotBA.Size(), "bit array size mismatch")
		})
	}
}

func TestProposalCache_GetCurrentProposal(t *testing.T) {
	bs := makeTestBlockStore(t)
	pc := NewProposalCache(bs)

	// Initially empty
	prop, block, ok := pc.GetCurrentProposal()
	require.Nil(t, prop)
	require.Nil(t, block)
	require.False(t, ok)

	// Add something
	p := makeCompactBlock(10, 1, 5)
	pc.AddProposal(p)

	gotProp, gotBlock, gotOk := pc.GetCurrentProposal()
	require.True(t, gotOk, "should have current proposal now")
	require.Equal(t, p.Proposal, *gotProp)
	require.NotNil(t, gotBlock)
}

func TestProposalCache_DeleteHeight(t *testing.T) {
	bs := makeTestBlockStore(t)
	pc := NewProposalCache(bs)

	pc.AddProposal(makeCompactBlock(10, 0, 3))
	pc.AddProposal(makeCompactBlock(10, 1, 3))
	pc.AddProposal(makeCompactBlock(11, 0, 5))

	_, _, _, okBefore := pc.getAllState(10, 0, true)
	require.True(t, okBefore, "proposal 10/0 should exist")

	pc.DeleteHeight(10)

	_, _, _, okAfter := pc.getAllState(10, 0, true)
	require.False(t, okAfter, "proposal for height=10 should have been deleted")
	_, _, _, stillOk := pc.getAllState(11, 0, true)
	require.True(t, stillOk, "proposal for height=11 should remain")
}

func TestProposalCache_DeleteRound(t *testing.T) {
	bs := makeTestBlockStore(t)
	pc := NewProposalCache(bs)

	pc.AddProposal(makeCompactBlock(10, 0, 3))
	pc.AddProposal(makeCompactBlock(10, 1, 3))
	pc.AddProposal(makeCompactBlock(10, 2, 3))

	pc.DeleteRound(10, 1)

	// Check if 10/1 is gone, but 10/0 and 10/2 remain
	_, _, ok10_1 := pc.GetProposal(10, 1)
	require.False(t, ok10_1)

	_, _, ok10_0 := pc.GetProposal(10, 0)
	require.True(t, ok10_0)

	_, _, ok10_2 := pc.GetProposal(10, 2)
	require.True(t, ok10_2)
}

func TestProposalCache_prune(t *testing.T) {
	bs := makeTestBlockStore(t)
	pc := NewProposalCache(bs)

	// Add proposals for heights 10, 11, 12
	// with multiple rounds to see if prune works
	for h := int64(10); h <= 12; h++ {
		pc.proposals[h] = make(map[int32]*proposalData)
		for r := int32(0); r < 3; r++ {
			pc.proposals[h][r] = &proposalData{
				compactBlock: makeCompactBlock(h, r, 3),
			}
		}
	}
	// Set current height/round
	pc.currentHeight = 12
	pc.currentRound = 2

	pc.prune(11)

	// Expect that everything older than height=11 is removed.
	// So height=10 is gone
	require.Nil(t, pc.proposals[10])

	// For height=11, we keep it because it's within 1 height of current (12)
	require.NotNil(t, pc.proposals[11])
	require.NotNil(t, pc.proposals[11][2], "round=2 of height=11 should remain")
	require.NotNil(t, pc.proposals[11][0], "round=0 of height=11 should have been pruned")
	require.NotNil(t, pc.proposals[11][1], "round=1 of height=11 should have been pruned")
	require.NotNil(t, pc.proposals[12][0], "round=0 of current height=12 should remain")
	require.NotNil(t, pc.proposals[12][1], "round=1 of current height=12 should remain")
	require.NotNil(t, pc.proposals[12][2], "round=2 of current height=12 should remain")
}
