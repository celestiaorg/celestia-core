package propagation

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/stretchr/testify/assert"
	proptypes "github.com/tendermint/tendermint/consensus/propagation/types"
	cmtrand "github.com/tendermint/tendermint/libs/rand"
	"github.com/tendermint/tendermint/types"
)

func TestCatchup(t *testing.T) {
	reactors, _ := testBlockPropReactors(3)
	reactor1 := reactors[0]
	reactor2 := reactors[1]
	reactor3 := reactors[2]

	// setting the proposal for height 8 round 1
	compactBlock := createCompactBlock(8, 1)
	reactor1.AddProposal(compactBlock)
	reactor2.AddProposal(compactBlock)
	reactor3.AddProposal(compactBlock)

	// setting the proposal for height 9 round 0
	compactBlock = createCompactBlock(9, 1)
	reactor1.AddProposal(compactBlock)
	reactor2.AddProposal(compactBlock)
	reactor3.AddProposal(compactBlock)

	// setting the proposal for height 10 round 0
	compactBlock = createCompactBlock(10, 0)
	reactor1.AddProposal(compactBlock)
	reactor2.AddProposal(compactBlock)
	reactor3.AddProposal(compactBlock)

	// setting the proposal for height 10 round 1
	compactBlock = createCompactBlock(10, 1)
	reactor1.AddProposal(compactBlock)
	reactor2.AddProposal(compactBlock)
	reactor3.AddProposal(compactBlock)

	// setting the first reactor current height and round
	reactor1.currentHeight = 8
	reactor1.currentRound = 0

	// handle the compact block
	reactor1.handleCompactBlock(compactBlock, reactor1.self)

	time.Sleep(200 * time.Millisecond)

	// check if reactor 1 sent wants to all the connected peers
	wants, has := reactor2.getPeer(reactor1.self).GetWants(9, 1)
	require.True(t, has)
	assert.Equal(t, 9, int(wants.Height))
	assert.Equal(t, 1, int(wants.Round))

	wants, has = reactor2.getPeer(reactor1.self).GetWants(10, 0)
	require.True(t, has)
	assert.Equal(t, 10, int(wants.Height))
	assert.Equal(t, 0, int(wants.Round))

	wants, has = reactor3.getPeer(reactor1.self).GetWants(9, 1)
	require.True(t, has)
	assert.Equal(t, 9, int(wants.Height))
	assert.Equal(t, 1, int(wants.Round))

	wants, has = reactor3.getPeer(reactor1.self).GetWants(10, 0)
	require.True(t, has)
	assert.Equal(t, 10, int(wants.Height))
	assert.Equal(t, 0, int(wants.Round))
}

func createCompactBlock(height int64, round int32) *proptypes.CompactBlock {
	return &proptypes.CompactBlock{
		BpHash:    cmtrand.Bytes(32),
		Signature: cmtrand.Bytes(64),
		LastLen:   0,
		Blobs: []proptypes.TxMetaData{
			{Hash: cmtrand.Bytes(32)},
			{Hash: cmtrand.Bytes(32)},
		},
		Proposal: types.Proposal{
			BlockID: types.BlockID{
				Hash:          nil,
				PartSetHeader: types.PartSetHeader{Total: 30},
			},
			Height: height,
			Round:  round,
		},
	}
}
