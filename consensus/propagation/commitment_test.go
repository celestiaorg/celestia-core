package propagation

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/stretchr/testify/require"
	proptypes "github.com/tendermint/tendermint/consensus/propagation/types"
	cmtrand "github.com/tendermint/tendermint/libs/rand"
	"github.com/tendermint/tendermint/state"
	types "github.com/tendermint/tendermint/types"
)

func TestPropose(t *testing.T) {
	reactors, _ := testBlockPropReactors(3)
	reactor1 := reactors[0]

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

	time.Sleep(4000 * time.Millisecond)

	for _, reactor := range reactors {
		_, parts, has := reactor.GetProposal(prop.Height, prop.Round)
		require.True(t, has)
		// check if the data is received in the reactor
		assert.True(t, parts.IsComplete())
	}
}
