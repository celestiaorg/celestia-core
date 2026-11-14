package state_test

import (
	"bytes"
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	dbm "github.com/cometbft/cometbft-db"

	abci "github.com/cometbft/cometbft/abci/types"
	cmtcfg "github.com/cometbft/cometbft/config"
	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/mempool"
	"github.com/cometbft/cometbft/mempool/cat"
	"github.com/cometbft/cometbft/proxy"
	sm "github.com/cometbft/cometbft/state"
	"github.com/cometbft/cometbft/store"
	cmttypes "github.com/cometbft/cometbft/types"
)

// gatedRecheckApp makes each recheck take a gate token (or wait for the gate to close), then
// rejects "stale" txs.
type gatedRecheckApp struct {
	abci.BaseApplication
	gate     chan struct{}
	rechecks atomic.Int32
}

func (app *gatedRecheckApp) CheckTx(_ context.Context, req *abci.RequestCheckTx) (*abci.ResponseCheckTx, error) {
	if req.Type == abci.CheckTxType_Recheck {
		app.rechecks.Add(1)
		<-app.gate
		if bytes.HasPrefix(req.Tx, []byte("stale")) {
			return &abci.ResponseCheckTx{Code: 1}, nil
		}
	}
	return &abci.ResponseCheckTx{Code: abci.CodeTypeOK, GasWanted: 1}, nil
}

func newGatedCATExecutor(t *testing.T, async bool) (*sm.BlockExecutor, *cat.TxPool, *gatedRecheckApp, sm.State, map[string]cmttypes.PrivValidator) {
	t.Helper()
	proxyApp := proxy.NewAppConns(proxy.NewLocalClientCreator(&testApp{}), proxy.NopMetrics())
	require.NoError(t, proxyApp.Start())
	t.Cleanup(func() { _ = proxyApp.Stop() })

	mpApp := &gatedRecheckApp{gate: make(chan struct{})}
	mpConn, err := proxy.NewLocalClientCreator(mpApp).NewABCIClient()
	require.NoError(t, err)
	require.NoError(t, mpConn.Start())
	t.Cleanup(func() { _ = mpConn.Stop() })
	cfg := cmtcfg.TestMempoolConfig()
	cfg.AsyncRecheck = async
	mp := cat.NewTxPool(log.NewNopLogger(), cfg, mpConn, 0)

	state, stateDB, privVals := makeState(1, 1)
	stateStore := sm.NewStore(stateDB, sm.StoreOptions{DiscardABCIResponses: false})
	blockExec := sm.NewBlockExecutor(stateStore, log.TestingLogger(), proxyApp.Consensus(),
		mp, sm.EmptyEvidencePool{}, store.NewBlockStore(dbm.NewMemDB()))
	return blockExec, mp, mpApp, state, privVals
}

// ApplyBlock returns while the recheck pass still holds the mempool lock, with
// the block's txs already removed from the pool.
func TestApplyBlockAsyncRecheck(t *testing.T) {
	blockExec, mp, mpApp, state, _ := newGatedCATExecutor(t, true)

	block, bps, err := makeBlock(state, 1, new(cmttypes.Commit))
	require.NoError(t, err)
	for _, tx := range block.Txs {
		require.NoError(t, mp.CheckTx(tx, nil, mempool.TxInfo{}))
	}
	pending := cmttypes.Tx("pending")
	require.NoError(t, mp.CheckTx(pending, nil, mempool.TxInfo{}))

	blockID := cmttypes.BlockID{Hash: block.Hash(), PartSetHeader: bps.Header()}
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

// A proposal made while the pass runs returns at once with only the txs the pass
// has kept; after the pass it equals the synchronous recheck's.
func TestCreateProposalBlockDuringRecheck(t *testing.T) {
	pending := cmttypes.Txs{cmttypes.Tx("pending-1"), cmttypes.Tx("stale-1"), cmttypes.Tx("pending-2")}
	setup := func(async bool) (func() cmttypes.Txs, *cat.TxPool, *gatedRecheckApp) {
		blockExec, mp, mpApp, state, privVals := newGatedCATExecutor(t, async)
		if !async {
			close(mpApp.gate)
		}
		block, bps, err := makeBlock(state, 1, new(cmttypes.Commit))
		require.NoError(t, err)
		for _, txs := range []cmttypes.Txs{block.Txs, pending} {
			for _, tx := range txs {
				require.NoError(t, mp.CheckTx(tx, nil, mempool.TxInfo{}))
			}
		}
		blockID := cmttypes.BlockID{Hash: block.Hash(), PartSetHeader: bps.Header()}
		state, err = blockExec.ApplyBlock(state, blockID, block, nil) // async: returns with the pass gated
		require.NoError(t, err)

		state.ConsensusParams.ABCI.VoteExtensionsEnableHeight = 1 // makeValidCommit signs extensions
		commit, _, err := makeValidCommit(1, blockID, state.Validators, privVals)
		require.NoError(t, err)
		proposer, _ := state.Validators.GetByIndex(0)
		propose := func() cmttypes.Txs {
			type result struct {
				block *cmttypes.Block
				err   error
			}
			proposed := make(chan result, 1)
			go func() {
				b, _, err := blockExec.CreateProposalBlock(context.Background(), 2, state, commit, proposer)
				proposed <- result{b, err}
			}()
			select {
			case r := <-proposed:
				require.NoError(t, r.err)
				return r.block.Txs
			case <-time.After(5 * time.Second):
				t.Fatal("proposal waited for the recheck pass")
				return nil
			}
		}
		return propose, mp, mpApp
	}
	proposeSync, _, _ := setup(false)
	want := proposeSync()
	require.Equal(t, cmttypes.Txs{cmttypes.Tx("pending-1"), cmttypes.Tx("pending-2")}, want)

	propose, mp, mpApp := setup(true)
	require.Empty(t, propose())
	mpApp.gate <- struct{}{} // pending-1 kept; the pass now holds at stale-1
	require.Eventually(t, func() bool { return mpApp.rechecks.Load() == 2 }, 5*time.Second, time.Millisecond)
	require.Equal(t, cmttypes.Txs{cmttypes.Tx("pending-1")}, propose())
	mpApp.gate <- struct{}{} // stale-1 evicted; the pass now holds at pending-2
	require.Eventually(t, func() bool { return mpApp.rechecks.Load() == 3 }, 5*time.Second, time.Millisecond)
	require.Equal(t, cmttypes.Txs{cmttypes.Tx("pending-1")}, propose())
	close(mpApp.gate)
	mp.Lock()
	mp.Unlock() //nolint:staticcheck // empty critical section waits for the pass
	require.Equal(t, want, propose())
}
