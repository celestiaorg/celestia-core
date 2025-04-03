package propagation

import (
	"testing"
	"time"

	dbm "github.com/cometbft/cometbft-db"
	"github.com/tendermint/tendermint/pkg/trace"
	"github.com/tendermint/tendermint/store"

	"github.com/stretchr/testify/assert"

	"github.com/stretchr/testify/require"
	cfg "github.com/tendermint/tendermint/config"
	proptypes "github.com/tendermint/tendermint/consensus/propagation/types"
	cmtrand "github.com/tendermint/tendermint/libs/rand"
	"github.com/tendermint/tendermint/state"
	"github.com/tendermint/tendermint/types"
)

func TestPropose(t *testing.T) {
	reactors, _ := testBlockPropReactors(3, cfg.DefaultP2PConfig())
	reactor1 := reactors[0]
	reactor2 := reactors[1]
	reactor3 := reactors[2]

	cleanup, _, sm := state.SetupTestCase(t)
	t.Cleanup(func() {
		cleanup(t)
	})

	prop, partSet, _, metaData := createTestProposal(sm, 1, 100, 1000)

	reactor1.ProposeBlock(prop, partSet, metaData)

	time.Sleep(200 * time.Millisecond)

	// check that the proposal was saved in reactor 1
	_, _, has := reactor1.GetProposal(prop.Height, prop.Round)
	require.True(t, has)

	// Check that the proposal was received by the other reactors
	_, _, has = reactor2.GetProposal(prop.Height, prop.Round)
	require.True(t, has)
	_, _, has = reactor3.GetProposal(prop.Height, prop.Round)
	require.True(t, has)

	// Check if the other reactors received the haves
	haves, has := reactor2.getPeer(reactor1.self).GetHaves(prop.Height, prop.Round)
	assert.True(t, has)
	// the parts == total because we only have 2 peers
	assert.Equal(t, haves.Size(), int(partSet.Total()*2))

	haves, has = reactor3.getPeer(reactor1.self).GetHaves(prop.Height, prop.Round)
	assert.True(t, has)
	// the parts == total because we only have 2 peers
	assert.Equal(t, haves.Size(), int(partSet.Total()*2))

	time.Sleep(500 * time.Millisecond)

	for _, r := range reactors {
		_, parts, _, has := r.getAllState(prop.Height, prop.Round, false)
		require.True(t, has)
		assert.True(t, parts.IsComplete())
	}

}

func createTestProposal(
	sm state.State,
	height int64,
	txCount, txSize int,
) (*types.Proposal, *types.PartSet, *types.Block, []proptypes.TxMetaData) {
	txs := make([]types.Tx, txCount)
	for i := 0; i < txCount; i++ {
		txs[i] = cmtrand.Bytes(txSize)
	}
	data := types.Data{
		Txs: txs,
	}
	block, partSet := sm.MakeBlock(height, data, types.RandCommit(time.Now()), []types.Evidence{}, cmtrand.Bytes(20))
	metaData := make([]proptypes.TxMetaData, len(partSet.TxPos))
	for i, pos := range partSet.TxPos {
		metaData[i] = proptypes.TxMetaData{
			Start: uint32(pos.Start),
			End:   uint32(pos.End),
			Hash:  block.Txs[i].Hash(),
		}
	}
	id := types.BlockID{Hash: block.Hash(), PartSetHeader: partSet.Header()}
	prop := types.NewProposal(block.Height, 0, 0, id)
	prop.Signature = cmtrand.Bytes(64)
	return prop, partSet, block, metaData
}

// TestRecoverPartsLocally provides a set of transactions to the mempool
// and attempts to build the block parts from them.
func TestRecoverPartsLocally(t *testing.T) {
	cleanup, _, sm := state.SetupTestCase(t)
	t.Cleanup(func() {
		cleanup(t)
	})

	numberOfTxs := 10
	txsMap := make(map[types.TxKey]*types.CachedTx)
	txs := make([]*types.CachedTx, numberOfTxs)
	for i := 0; i < numberOfTxs; i++ {
		tx := &types.CachedTx{Tx: cmtrand.Bytes(int(types.BlockPartSizeBytes / 3))}
		txKey, err := types.TxKeyFromBytes(tx.Hash())
		require.NoError(t, err)
		txsMap[txKey] = tx
		txs[i] = tx
	}

	blockStore := store.NewBlockStore(dbm.NewMemDB())
	blockPropR := NewReactor("", trace.NoOpTracer(), blockStore, &mockMempool{
		txs: txsMap,
	})

	data := types.Data{Txs: types.TxsFromCachedTxs(txs)}

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

	blockPropR.ProposeBlock(prop, partSet, metaData)

	_, actualParts, _ := blockPropR.GetProposal(prop.Height, prop.Round)

	// we should be able to recover all the parts after where the transactions
	// are encoded
	startingPartIndex := metaData[0].Start/types.BlockPartSizeBytes + 1

	for i := startingPartIndex; i < partSet.Total()-1; i++ {
		apart := actualParts.GetPart(int(i))
		require.NotNil(t, apart)
		assert.Equal(t, partSet.GetPart(int(i)).Bytes, apart.Bytes)
	}
}

var _ Mempool = &mockMempool{}

type mockMempool struct {
	txs map[types.TxKey]*types.CachedTx
}

func (m *mockMempool) AddTx(tx types.Tx) {
	cachTx := &types.CachedTx{Tx: tx}
	m.txs[tx.Key()] = cachTx
}

func (m *mockMempool) GetTxByKey(key types.TxKey) (*types.CachedTx, bool) {
	val, found := m.txs[key]
	return val, found
}
