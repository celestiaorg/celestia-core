package propagation

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	proptypes "github.com/tendermint/tendermint/consensus/propagation/types"
	"github.com/tendermint/tendermint/crypto/merkle"
	"github.com/tendermint/tendermint/libs/bits"
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

	// setting the proposal for height 9 round 1
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

	// setting the requests for reactor 2 in reactor 1
	fullBitArray := bits.NewBitArray(int(compactBlock.Proposal.BlockID.PartSetHeader.Total))
	fullBitArray.Fill()
	reactor1.getPeer(reactor2.self).AddRequests(10, 0, fullBitArray)

	// handle the compact block of height 10 round 1
	reactor1.handleCompactBlock(compactBlock, reactor2.self)
	reactor1.handleHaves(reactor2.self, &proptypes.HaveParts{
		Height: compactBlock.Proposal.Height,
		Round:  compactBlock.Proposal.Round,
		Parts: []proptypes.PartMetaData{
			createPartMetaData(0, 5),
			createPartMetaData(1, 5),
			createPartMetaData(2, 5),
		},
	}, true)

	time.Sleep(500 * time.Millisecond)

	// check if reactor 2 received the want for height 10 round 1 because it provided the haves
	_, has := reactor2.getPeer(reactor1.self).GetWants(10, 1)
	assert.True(t, has)

	// check if reactor 1 sent the retry for height 9 round 1
	_, has = reactor2.getPeer(reactor1.self).GetWants(9, 1)
	assert.True(t, has)

	// check that reactor 1 didn't send a retry want for height 10 round 0
	// because we manually set that it already sent it to it
	_, has = reactor2.getPeer(reactor1.self).GetWants(10, 0)
	assert.False(t, has)

	// check if reactor 1 sent the retry for height 9 round 1
	_, has = reactor3.getPeer(reactor1.self).GetWants(9, 1)
	assert.True(t, has)

	// check if reactor 1 sent the retry for height 10 round 0
	_, has = reactor3.getPeer(reactor1.self).GetWants(10, 0)
	assert.True(t, has)
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

// createPartMetaData creates a part metadata using the provided index and total.
// the remaining values are random.
func createPartMetaData(index uint32, total int64) proptypes.PartMetaData {
	return proptypes.PartMetaData{
		Index: index,
		Hash:  cmtrand.Bytes(32),
		Proof: merkle.Proof{
			Total:    total,
			Index:    int64(index),
			LeafHash: cmtrand.Bytes(32),
			Aunts:    [][]byte{cmtrand.Bytes(32), cmtrand.Bytes(32)},
		},
	}
}
