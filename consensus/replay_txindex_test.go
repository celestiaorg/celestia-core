package consensus

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	dbm "github.com/cometbft/cometbft-db"

	abci "github.com/cometbft/cometbft/abci/types"
	"github.com/cometbft/cometbft/crypto/merkle"
	"github.com/cometbft/cometbft/internal/test"
	"github.com/cometbft/cometbft/privval"
	"github.com/cometbft/cometbft/proxy"
	sm "github.com/cometbft/cometbft/state"
	blockidxnull "github.com/cometbft/cometbft/state/indexer/block/null"
	"github.com/cometbft/cometbft/state/txindex"
	"github.com/cometbft/cometbft/state/txindex/kv"
	"github.com/cometbft/cometbft/types"
)

// TestHandshakeReplayReindexesTxResults replays a block after a rollback with an app that
// now fails its txs, and checks that the tx index returns the new results.
func TestHandshakeReplayReindexesTxResults(t *testing.T) {
	config := ResetConfig("handshake_txindex_test_")
	defer os.RemoveAll(config.RootDir)
	privVal := privval.LoadFilePV(config.PrivValidatorKeyFile(), config.PrivValidatorStateFile())
	stateDB, state, store := stateAndStore(t, config, 0x0)
	stateStore := sm.NewStore(stateDB, sm.StoreOptions{DiscardABCIResponses: false})
	genDoc, err := sm.MakeGenesisDocFromFile(config.GenesisFile())
	require.NoError(t, err)
	state.LastValidators = state.Validators.Copy()
	// The handshake runs InitChain first, which sets this.
	state.LastResultsHash = merkle.HashFromByteSlices(nil)

	// The block store is one block ahead of the state and the app, as after a rollback.
	block, _, err := state.MakeBlock(state.InitialHeight, types.MakeData(test.MakeNTxs(state.InitialHeight, 10)),
		&types.Commit{}, nil, state.Validators.Proposer.Address)
	require.NoError(t, err)
	seenCommit, err := test.MakeCommit(types.BlockID{Hash: block.Hash()}, block.Height, 0,
		state.Validators, []types.PrivValidator{privVal}, state.ChainID, block.Time)
	require.NoError(t, err)
	store.chain = []*types.Block{block}
	store.extCommits = []*types.ExtendedCommit{seenCommit.WrappedExtendedCommit()}

	eventBus := types.NewEventBus()
	require.NoError(t, eventBus.Start())
	t.Cleanup(func() { _ = eventBus.Stop() })

	// The run before the rollback indexed every tx as successful.
	txIndexer := kv.NewTxIndex(dbm.NewMemDB())
	for i, tx := range block.Txs {
		require.NoError(t, txIndexer.Index(&abci.TxResult{
			Height: block.Height,
			Index:  uint32(i),
			Tx:     tx,
			Result: abci.ExecTxResult{Code: abci.CodeTypeOK},
		}))
	}
	indexerService := txindex.NewIndexerService(txIndexer, &blockidxnull.BlockerIndexer{}, eventBus, false)
	require.NoError(t, indexerService.Start())
	t.Cleanup(func() { _ = indexerService.Stop() })

	proxyApp := proxy.NewAppConns(proxy.NewLocalClientCreator(&failTxsApp{}), proxy.NopMetrics())
	require.NoError(t, proxyApp.Start())
	t.Cleanup(func() { _ = proxyApp.Stop() })

	h := NewHandshaker(stateStore, state, store, genDoc)
	h.SetEventBus(eventBus)
	_, err = h.Handshake(proxyApp)
	require.NoError(t, err)

	for _, tx := range block.Txs {
		require.Eventually(t, func() bool {
			res, err := txIndexer.Get(tx.Hash())
			return err == nil && res != nil && res.Result.Code == failTxsCode
		}, 5*time.Second, 10*time.Millisecond, "tx %X kept its result from before the rollback", tx.Hash())
	}
}

const failTxsCode = 1

// failTxsApp fails every tx.
type failTxsApp struct {
	abci.BaseApplication
}

func (*failTxsApp) FinalizeBlock(_ context.Context, req *abci.RequestFinalizeBlock) (*abci.ResponseFinalizeBlock, error) {
	results := make([]*abci.ExecTxResult, len(req.Txs))
	for i := range results {
		results[i] = &abci.ExecTxResult{Code: failTxsCode}
	}
	return &abci.ResponseFinalizeBlock{TxResults: results}, nil
}
