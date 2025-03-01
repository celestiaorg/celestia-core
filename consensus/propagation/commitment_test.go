package propagation

import (
	"github.com/stretchr/testify/assert"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	proptypes "github.com/tendermint/tendermint/consensus/propagation/types"
	cmtrand "github.com/tendermint/tendermint/libs/rand"
	"github.com/tendermint/tendermint/state"
	types "github.com/tendermint/tendermint/types"
)

func TestPropose(t *testing.T) {
	reactors, _ := testBlockPropReactors(3)
	reactor1 := reactors[0]
	reactor2 := reactors[1]
	reactor3 := reactors[2]

	cleanup, _, sm := state.SetupTestCase(t)
	t.Cleanup(func() {
		cleanup(t)
	})

	data := types.Data{
		Txs: []types.Tx{
			cmtrand.Bytes(1000),
			cmtrand.Bytes(64000),
			cmtrand.Bytes(2000000),
		},
	}

	block, partSet := sm.MakeBlock(1, data, types.RandCommit(time.Now()), []types.Evidence{}, cmtrand.Bytes(20))
	id := types.BlockID{Hash: block.Hash(), PartSetHeader: partSet.Header()}
	prop := types.NewProposal(block.Height, 0, 0, id)
	prop.Signature = cmtrand.Bytes(64)

	metaData := make([]proptypes.TxMetaData, len(partSet.TxPos))
	for i, pos := range partSet.TxPos {
		metaData[i] = proptypes.TxMetaData{
			Start: uint32(pos.Start),
			End:   uint32(pos.End),
			Hash:  block.Txs[i].Hash(),
		}
	}

	reactor1.ProposeBlock(prop, partSet, metaData)

	time.Sleep(400 * time.Millisecond)

	// Check that the proposal was received by the other reactors
	_, _, has := reactor2.GetProposal(prop.Height, prop.Round)
	require.True(t, has)
	_, _, has = reactor3.GetProposal(prop.Height, prop.Round)
	require.True(t, has)

	// Check if the other reactors received the haves
	haves, has := reactor2.getPeer(reactor1.self).GetHaves(prop.Height, prop.Round)
	assert.True(t, has)
	// the parts == total because we only have 2 peers
	assert.Equal(t, len(haves.Parts), int(partSet.Total()))

	haves, has = reactor3.getPeer(reactor1.self).GetHaves(prop.Height, prop.Round)
	assert.True(t, has)
	// the parts == total because we only have 2 peers
	assert.Equal(t, len(haves.Parts), int(partSet.Total()))
}
