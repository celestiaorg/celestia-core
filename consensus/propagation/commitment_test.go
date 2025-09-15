package propagation

import (
	"testing"
	"time"

	dbm "github.com/cometbft/cometbft-db"
	"github.com/cometbft/cometbft/store"

	"github.com/stretchr/testify/assert"

	"github.com/stretchr/testify/require"

	cfg "github.com/cometbft/cometbft/config"
	proptypes "github.com/cometbft/cometbft/consensus/propagation/types"
	cmtrand "github.com/cometbft/cometbft/libs/rand"
	"github.com/cometbft/cometbft/state"
	"github.com/cometbft/cometbft/types"
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

	prop, partSet, _, metaData := createTestProposal(t, sm, 1, 0, 100, 1000)

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
	for _, index := range haves.GetTrueIndices() {
		assert.GreaterOrEqual(t, index, int(partSet.Total()))
	}

	haves, has = reactor3.getPeer(reactor1.self).GetHaves(prop.Height, prop.Round)
	assert.True(t, has)
	// the parts == total because we only have 2 peers
	assert.Equal(t, haves.Size(), int(partSet.Total()*2))
	for _, index := range haves.GetTrueIndices() {
		assert.GreaterOrEqual(t, index, int(partSet.Total()))
	}

	time.Sleep(500 * time.Millisecond)

	for _, r := range reactors {
		_, parts, _, has := r.getAllState(prop.Height, prop.Round, false)
		require.True(t, has)
		assert.True(t, parts.IsComplete())
	}
}

func TestPropose_OnlySendParityChunks(t *testing.T) {
	reactors, _ := testBlockPropReactors(2, cfg.DefaultP2PConfig())
	reactor1 := reactors[0]
	reactor2 := reactors[1]

	cleanup, _, sm := state.SetupTestCase(t)
	t.Cleanup(func() {
		cleanup(t)
	})

	// 128 mb block
	prop, partSet, _, metaData := createTestProposal(t, sm, 1, 0, 30, 4_000_000)

	reactor1.ProposeBlock(prop, partSet, metaData)

	time.Sleep(200 * time.Millisecond)

	// check that the proposal was saved in reactor 1
	_, _, has := reactor1.GetProposal(prop.Height, prop.Round)
	require.True(t, has)

	// Check that the proposal was received by the other reactors
	_, _, has = reactor2.GetProposal(prop.Height, prop.Round)
	require.True(t, has)

	// Check whether all the received haves are for parity parts
	haves, has := reactor2.getPeer(reactor1.self).GetHaves(prop.Height, prop.Round)
	assert.True(t, has)
	// the parts == total because we only have 2 peers
	assert.Equal(t, haves.Size(), int(partSet.Total()*2))
	for _, index := range haves.GetTrueIndices() {
		assert.GreaterOrEqual(t, index, int(partSet.Total()))
	}
}

func createTestProposal(
	t *testing.T,
	sm state.State,
	height int64,
	round int32,
	txCount, txSize int,
) (*types.Proposal, *types.PartSet, *types.Block, []proptypes.TxMetaData) {
	txs := make([]types.Tx, txCount)
	for i := 0; i < txCount; i++ {
		txs[i] = cmtrand.Bytes(txSize)
	}
	data := types.Data{
		Txs: txs,
	}
	block, partSet, err := sm.MakeBlock(height, data, types.RandCommit(time.Now()), []types.Evidence{}, cmtrand.Bytes(20))
	require.NoError(t, err)
	metaData := make([]proptypes.TxMetaData, len(partSet.TxPos))
	for i, pos := range partSet.TxPos {
		metaData[i] = proptypes.TxMetaData{
			Start: pos.Start,
			End:   pos.End,
			Hash:  block.Txs[i].Hash(),
		}
	}
	id := types.BlockID{Hash: block.Hash(), PartSetHeader: partSet.Header()}
	prop := types.NewProposal(block.Height, round, -1, id)
	protoProp := prop.ToProto()
	err = mockPrivVal.SignProposal(TestChainID, protoProp)
	require.NoError(t, err)
	prop.Signature = protoProp.Signature
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
	blockPropR := NewReactor(
		"",
		Config{
			Store: blockStore,
			Mempool: &mockMempool{
				txs: txsMap,
			},
			Privval:       mockPrivVal,
			ChainID:       sm.ChainID,
			BlockMaxBytes: sm.ConsensusParams.Block.MaxBytes,
		},
	)
	blockPropR.currentProposer = mockPubKey

	data := types.Data{Txs: types.TxsFromCachedTxs(txs)}

	block, partSet, err := sm.MakeBlock(1, data, types.RandCommit(time.Now()), []types.Evidence{}, cmtrand.Bytes(20))
	require.NoError(t, err)
	id := types.BlockID{Hash: block.Hash(), PartSetHeader: partSet.Header()}
	prop := types.NewProposal(block.Height, 0, -1, id)
	protoProp := prop.ToProto()
	err = mockPrivVal.SignProposal("test-chain", protoProp)
	require.NoError(t, err)
	prop.Signature = protoProp.Signature

	metaData := make([]proptypes.TxMetaData, len(partSet.TxPos))
	for i, pos := range partSet.TxPos {
		metaData[i] = proptypes.TxMetaData{
			Start: pos.Start,
			End:   pos.End,
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
