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
	"github.com/cometbft/cometbft/libs/log"
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

	prop, partSet, _, metaData := createTestProposal(t, sm, 1, 100, 1000)

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
	t *testing.T,
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
	prop := types.NewProposal(block.Height, 0, -1, id)
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

// TestCompactBlockSigningFailure tests that appropriate error messages are logged
// when compact block signing fails, indicating potential KMS compatibility issues.
func TestCompactBlockSigningFailure(t *testing.T) {
	cleanup, _, sm := state.SetupTestCase(t)
	t.Cleanup(func() {
		cleanup(t)
	})

	// Create a mock PrivValidator that always fails to sign raw bytes
	failingPrivVal := &types.ErroringMockPV{}

	// Use a buffer to capture log output
	var logBuffer bytes.Buffer
	logger := log.NewTMLogger(&logBuffer)

	blockStore := store.NewBlockStore(dbm.NewMemDB())
	blockPropR := NewReactor(
		"test-id",
		Config{
			Store:         blockStore,
			Mempool:       &mockMempool{txs: make(map[types.TxKey]*types.CachedTx)},
			Privval:       failingPrivVal,
			ChainID:       TestChainID,
			BlockMaxBytes: 100000000,
		},
	)
	blockPropR.SetLogger(logger)
	blockPropR.currentProposer = mockPubKey

	// Create a test proposal
	prop, partSet, _, metaData := createTestProposal(t, sm, 1, 10, 100)

	// Attempt to propose the block - this should fail and log appropriate errors
	blockPropR.ProposeBlock(prop, partSet, metaData)

	// Check that the appropriate error messages were logged
	logOutput := logBuffer.String()

	// Verify that the enhanced error message is present
	assert.Contains(t, logOutput, "failed to sign compact block - this may indicate an incompatible KMS version")
	assert.Contains(t, logOutput, "incompatible KMS version that does not support compact block signing")
	assert.Contains(t, logOutput, "height=1")
	assert.Contains(t, logOutput, "round=0")
	assert.Contains(t, logOutput, "chain_id="+TestChainID)
	assert.Contains(t, logOutput, "unique_id="+CompactBlockUID)

	// Verify that the info message about retrying is present
	assert.Contains(t, logOutput, "compact block signing will be retried on the next proposal")
	assert.Contains(t, logOutput, "ensure KMS supports compact block signing for v5 compatibility")

	// Verify that no compact block was created (proposal should not exist in reactor)
	_, _, has := blockPropR.GetProposal(prop.Height, prop.Round)
	assert.False(t, has, "compact block should not be created when signing fails")

	// Test that repeated attempts continue to log errors
	logBuffer.Reset()

	// Attempt another proposal - should log the same errors again
	prop2, partSet2, _, metaData2 := createTestProposal(t, sm, 2, 10, 100)
	blockPropR.ProposeBlock(prop2, partSet2, metaData2)

	logOutput2 := logBuffer.String()
	assert.Contains(t, logOutput2, "failed to sign compact block - this may indicate an incompatible KMS version")
	assert.Contains(t, logOutput2, "height=2")

	// Verify that this second attempt also didn't create a compact block
	_, _, has = blockPropR.GetProposal(prop2.Height, prop2.Round)
	assert.False(t, has, "compact block should not be created when signing fails on retry")
}
