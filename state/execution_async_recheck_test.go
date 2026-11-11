package state_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	dbm "github.com/cometbft/cometbft-db"

	abci "github.com/cometbft/cometbft/abci/types"
	"github.com/cometbft/cometbft/config"
	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/mempool"
	"github.com/cometbft/cometbft/mempool/cat"
	"github.com/cometbft/cometbft/proxy"
	sm "github.com/cometbft/cometbft/state"
	"github.com/cometbft/cometbft/store"
	"github.com/cometbft/cometbft/types"
)

// gatedRecheckApp blocks every recheck until the gate is closed.
type gatedRecheckApp struct {
	abci.BaseApplication
	gate chan struct{}
}

func (app *gatedRecheckApp) CheckTx(_ context.Context, req *abci.RequestCheckTx) (*abci.ResponseCheckTx, error) {
	if req.Type == abci.CheckTxType_Recheck {
		<-app.gate
	}
	return &abci.ResponseCheckTx{Code: abci.CodeTypeOK, GasWanted: 1}, nil
}

// ApplyBlock returns while the recheck pass still holds the mempool lock, with
// the block's txs already removed from the pool.
func TestApplyBlockAsyncRecheck(t *testing.T) {
	proxyApp := proxy.NewAppConns(proxy.NewLocalClientCreator(&testApp{}), proxy.NopMetrics())
	require.NoError(t, proxyApp.Start())
	defer proxyApp.Stop() //nolint:errcheck // ignore for tests

	mpApp := &gatedRecheckApp{gate: make(chan struct{})}
	mpConn, err := proxy.NewLocalClientCreator(mpApp).NewABCIClient()
	require.NoError(t, err)
	require.NoError(t, mpConn.Start())
	defer mpConn.Stop() //nolint:errcheck // ignore for tests
	cfg := config.TestMempoolConfig()
	cfg.AsyncRecheck = true
	mp := cat.NewTxPool(log.NewNopLogger(), cfg, mpConn, 0)

	state, stateDB, _ := makeState(1, 1)
	stateStore := sm.NewStore(stateDB, sm.StoreOptions{DiscardABCIResponses: false})
	blockExec := sm.NewBlockExecutor(stateStore, log.TestingLogger(), proxyApp.Consensus(),
		mp, sm.EmptyEvidencePool{}, store.NewBlockStore(dbm.NewMemDB()))

	block, bps, err := makeBlock(state, 1, new(types.Commit))
	require.NoError(t, err)
	for _, tx := range block.Txs {
		require.NoError(t, mp.CheckTx(tx, nil, mempool.TxInfo{}))
	}
	pending := types.Tx("pending")
	require.NoError(t, mp.CheckTx(pending, nil, mempool.TxInfo{}))

	blockID := types.BlockID{Hash: block.Hash(), PartSetHeader: bps.Header()}
	applied := make(chan error, 1)
	go func() {
		_, err := blockExec.ApplyBlock(state, blockID, block, nil)
		applied <- err
	}()
	select {
	case err := <-applied:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("ApplyBlock waited for the recheck pass")
	}
	require.Equal(t, 1, mp.Size())
	require.True(t, mp.Has(pending.Key()))

	locked := make(chan struct{})
	go func() {
		mp.Lock()
		close(locked)
		mp.Unlock()
	}()
	select {
	case <-locked:
		t.Fatal("mempool lock released before the recheck pass ended")
	case <-time.After(100 * time.Millisecond):
	}
	close(mpApp.gate)
	<-locked
}
