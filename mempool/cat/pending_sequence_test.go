package cat

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	abci "github.com/cometbft/cometbft/abci/types"
	"github.com/cometbft/cometbft/config"
	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/mempool"
	"github.com/cometbft/cometbft/proxy"
	"github.com/cometbft/cometbft/types"
)

// seqApp accepts txs of the form "signer/sequence/nonce" and reports the
// signer and sequence; txs listed in reject fail on recheck.
type seqApp struct {
	abci.BaseApplication
	mtx    sync.Mutex
	reject map[string]bool
}

func (app *seqApp) CheckTx(_ context.Context, req *abci.RequestCheckTx) (*abci.ResponseCheckTx, error) {
	parts := strings.Split(string(req.Tx), "/")
	if len(parts) != 3 {
		return &abci.ResponseCheckTx{Code: 1}, nil
	}
	seq, err := strconv.ParseUint(parts[1], 10, 64)
	if err != nil {
		return &abci.ResponseCheckTx{Code: 1}, nil
	}
	app.mtx.Lock()
	rejected := req.Type == abci.CheckTxType_Recheck && app.reject[string(req.Tx)]
	app.mtx.Unlock()
	if rejected {
		return &abci.ResponseCheckTx{Code: 2}, nil
	}
	return &abci.ResponseCheckTx{Address: []byte(parts[0]), Sequence: seq, GasWanted: 1, Priority: 1}, nil
}

func (app *seqApp) rejectOnRecheck(tx types.Tx) {
	app.mtx.Lock()
	defer app.mtx.Unlock()
	app.reject[string(tx)] = true
}

func seqTx(signer string, seq uint64) types.Tx {
	return types.Tx(fmt.Sprintf("%s/%d/x", signer, seq))
}

func setupSeqPool(t *testing.T) (*TxPool, *seqApp) {
	t.Helper()
	app := &seqApp{reject: make(map[string]bool)}
	conn, err := proxy.NewLocalClientCreator(app).NewABCIClient()
	require.NoError(t, err)
	require.NoError(t, conn.Start())
	cfg := config.TestMempoolConfig()
	cfg.Recheck = true
	t.Cleanup(func() {
		os.RemoveAll(cfg.RootDir)
		require.NoError(t, conn.Stop())
	})
	return NewTxPool(log.TestingLogger(), cfg, conn, 1), app
}

func addSeqTx(t *testing.T, txmp *TxPool, tx types.Tx) {
	t.Helper()
	rsp, err := txmp.TryAddNewTx(tx.ToCachedTx(), tx.Key(), mempool.TxInfo{})
	require.NoError(t, err)
	require.Equal(t, abci.CodeTypeOK, rsp.Code)
}

func commitTxs(t *testing.T, txmp *TxPool, height int64, txs ...types.Tx) {
	t.Helper()
	results := make([]*abci.ExecTxResult, len(txs))
	for i := range results {
		results[i] = &abci.ExecTxResult{Code: abci.CodeTypeOK}
	}
	txmp.Lock()
	defer txmp.Unlock()
	require.NoError(t, txmp.Update(height, types.CachedTxFromTxs(txs), results, nil, nil))
}

func TestStoreHighestSequence(t *testing.T) {
	s := newStore()
	signer := []byte("alice")
	_, ok := s.highestSequence(signer)
	require.False(t, ok)

	w3 := newWrappedTx(types.Tx("s3").ToCachedTx(), 1, 1, 1, signer, 3, false)
	w1 := newWrappedTx(types.Tx("s1").ToCachedTx(), 1, 1, 1, signer, 1, false)
	w2 := newWrappedTx(types.Tx("s2").ToCachedTx(), 1, 1, 1, signer, 2, false)
	for _, w := range []*wrappedTx{w3, w1, w2} {
		require.True(t, s.set(w))
	}
	// Txs without a signer are never indexed.
	require.True(t, s.set(newWrappedTx(types.Tx("anon").ToCachedTx(), 1, 1, 1, nil, 9, false)))
	_, ok = s.highestSequence(nil)
	require.False(t, ok)

	seq, ok := s.highestSequence(signer)
	require.True(t, ok)
	require.EqualValues(t, 3, seq)

	require.True(t, s.remove(w3.key()))
	seq, ok = s.highestSequence(signer)
	require.True(t, ok)
	require.EqualValues(t, 2, seq)

	_, purged := s.purgeExpiredTxs(2, time.Time{})
	require.Equal(t, 3, purged)
	_, ok = s.highestSequence(signer)
	require.False(t, ok)
}

func TestTxPoolHighestPendingSequence(t *testing.T) {
	txmp, app := setupSeqPool(t)
	for seq := uint64(5); seq <= 9; seq++ {
		addSeqTx(t, txmp, seqTx("alice", seq))
	}
	addSeqTx(t, txmp, seqTx("bob", 0))

	seq, ok := txmp.HighestPendingSequence([]byte("alice"))
	require.True(t, ok)
	require.EqualValues(t, 9, seq)
	seq, ok = txmp.HighestPendingSequence([]byte("bob"))
	require.True(t, ok)
	require.EqualValues(t, 0, seq)
	_, ok = txmp.HighestPendingSequence([]byte("carol"))
	require.False(t, ok)

	// Committing a prefix keeps the highest pending sequence.
	commitTxs(t, txmp, 2, seqTx("alice", 5), seqTx("alice", 6))
	seq, ok = txmp.HighestPendingSequence([]byte("alice"))
	require.True(t, ok)
	require.EqualValues(t, 9, seq)

	// A tx evicted by recheck no longer counts.
	app.rejectOnRecheck(seqTx("alice", 9))
	commitTxs(t, txmp, 3)
	seq, ok = txmp.HighestPendingSequence([]byte("alice"))
	require.True(t, ok)
	require.EqualValues(t, 8, seq)

	// Once everything is committed the signer has nothing pending.
	commitTxs(t, txmp, 4, seqTx("alice", 7), seqTx("alice", 8), seqTx("bob", 0))
	_, ok = txmp.HighestPendingSequence([]byte("alice"))
	require.False(t, ok)
	_, ok = txmp.HighestPendingSequence([]byte("bob"))
	require.False(t, ok)
}

// The accessor must not take txmp.mtx: Commit holds it across recheck, and the
// application calls the accessor from QuerySequence on the same connection.
func TestHighestPendingSequenceDoesNotTakePoolLock(t *testing.T) {
	txmp, _ := setupSeqPool(t)
	addSeqTx(t, txmp, seqTx("alice", 1))

	txmp.Lock()
	defer txmp.Unlock()
	done := make(chan uint64)
	go func() {
		seq, _ := txmp.HighestPendingSequence([]byte("alice"))
		done <- seq
	}()
	select {
	case seq := <-done:
		require.EqualValues(t, 1, seq)
	case <-time.After(5 * time.Second):
		t.Fatal("HighestPendingSequence blocked on the mempool lock")
	}
}

// Readers run against admission, commit and recheck; meant for -race.
func TestHighestPendingSequenceConcurrent(t *testing.T) {
	txmp, _ := setupSeqPool(t)
	const signers, perSigner = 4, 50

	var stop atomic.Bool
	var readers sync.WaitGroup
	for r := 0; r < 4; r++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for !stop.Load() {
				for s := 0; s < signers; s++ {
					_, _ = txmp.HighestPendingSequence([]byte(fmt.Sprintf("s%d", s)))
				}
			}
		}()
	}

	var writers sync.WaitGroup
	for s := 0; s < signers; s++ {
		writers.Add(1)
		go func(s int) {
			defer writers.Done()
			for seq := uint64(0); seq < perSigner; seq++ {
				tx := seqTx(fmt.Sprintf("s%d", s), seq)
				if _, err := txmp.TryAddNewTx(tx.ToCachedTx(), tx.Key(), mempool.TxInfo{}); err != nil {
					t.Error(err)
				}
			}
		}(s)
	}
	for h := int64(2); h < 12; h++ {
		reaped := txmp.ReapMaxTxs(10)
		txs := make([]types.Tx, len(reaped))
		for i, tx := range reaped {
			txs[i] = tx.Tx
		}
		commitTxs(t, txmp, h, txs...)
	}
	writers.Wait()
	stop.Store(true)
	readers.Wait()
}
