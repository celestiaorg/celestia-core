package cat

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	dbm "github.com/cometbft/cometbft-db"

	"github.com/cometbft/cometbft/abci/example/kvstore"
	abci "github.com/cometbft/cometbft/abci/types"
	cmtcfg "github.com/cometbft/cometbft/config"
	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/proxy"
)

// recheckCounter counts per-tx rechecks.
type recheckCounter struct {
	*application
	rechecks int
}

func (app *recheckCounter) CheckTx(ctx context.Context, req *abci.RequestCheckTx) (*abci.ResponseCheckTx, error) {
	if req.Type == abci.CheckTxType_Recheck {
		app.rechecks++
	}
	return app.application.CheckTx(ctx, req)
}

// batchApp answers RecheckTxs with verdict(tx) for every tx, or with reply if set.
type batchApp struct {
	*recheckCounter
	verdict func(tx []byte) *abci.ResponseCheckTx
	reply   func(n int) (*abci.ResponseRecheckTxs, error)
	batches [][][]byte
}

func (app *batchApp) RecheckTxs(_ context.Context, req *abci.RequestRecheckTxs) (*abci.ResponseRecheckTxs, error) {
	app.batches = append(app.batches, req.Txs)
	if app.reply != nil {
		return app.reply(len(req.Txs))
	}
	rsp := &abci.ResponseRecheckTxs{Responses: make([]*abci.ResponseCheckTx, len(req.Txs))}
	for i, tx := range req.Txs {
		rsp.Responses[i] = app.verdict(tx)
	}
	return rsp, nil
}

func newRecheckCounter() *recheckCounter {
	return &recheckCounter{application: &application{kvstore.NewApplication(dbm.NewMemDB())}}
}

func newBatchPool(t *testing.T, app abci.Application, disable bool, logger log.Logger) *TxPool {
	cfg := cmtcfg.TestMempoolConfig()
	cfg.Recheck = true
	if disable {
		cfg.DisableRecheckBatch = true
	}
	cli, err := proxy.NewLocalClientCreator(app).NewABCIClient()
	require.NoError(t, err)
	require.NoError(t, cli.Start())
	t.Cleanup(func() {
		os.RemoveAll(cfg.RootDir)
		require.NoError(t, cli.Stop())
	})
	return NewTxPool(logger, cfg, cli, 0)
}

// The default config uses the batch path when the app supports it.
func TestTxPool_RecheckBatch(t *testing.T) {
	require.False(t, cmtcfg.DefaultMempoolConfig().DisableRecheckBatch)
	app := &batchApp{recheckCounter: newRecheckCounter()}
	txmp := newBatchPool(t, app, false, log.TestingLogger())
	txs := checkTxs(t, txmp, 10, 0)
	app.verdict = func(tx []byte) *abci.ResponseCheckTx {
		switch {
		case bytes.Equal(tx, txs[2].tx), bytes.Equal(tx, txs[3].tx):
			return &abci.ResponseCheckTx{Code: 1}
		case bytes.Equal(tx, txs[4].tx):
			return nil // not rechecked: kept
		}
		return &abci.ResponseCheckTx{Code: abci.CodeTypeOK}
	}
	var want [][]byte
	for _, wtx := range txmp.store.getOrderedTxs() {
		if !bytes.Equal(wtx.tx.Tx, txs[0].tx) && !bytes.Equal(wtx.tx.Tx, txs[1].tx) {
			want = append(want, wtx.tx.Tx)
		}
	}

	commitTxs(t, txmp, 1, txs[0].tx, txs[1].tx)

	require.Equal(t, [][][]byte{want}, app.batches, "one batch in recheck order")
	require.Zero(t, app.rechecks)
	require.Equal(t, 6, txmp.Size())
	require.False(t, txmp.Has(txs[2].tx.Key()))
	require.False(t, txmp.Has(txs[3].tx.Key()))
	require.True(t, txmp.Has(txs[4].tx.Key()))
}

func TestTxPool_RecheckBatchDisabled(t *testing.T) {
	var logs bytes.Buffer
	app := &batchApp{recheckCounter: newRecheckCounter()}
	txmp := newBatchPool(t, app, true, log.NewTMLogger(log.NewSyncWriter(&logs)))
	txs := checkTxs(t, txmp, 10, 0)
	commitTxs(t, txmp, 1, txs[0].tx, txs[1].tx)

	require.Empty(t, app.batches)
	require.Equal(t, 8, app.rechecks)
	require.Equal(t, 8, txmp.Size())
	require.NotContains(t, logs.String(), "does not support RecheckTxs")
}

// An app without RecheckTxs, or one that answers ErrRecheckTxsUnsupported, is
// rechecked per tx and the fallback is logged once.
func TestTxPool_RecheckBatchUnsupported(t *testing.T) {
	for _, tc := range []struct {
		name      string
		withIface bool
	}{{"no interface", false}, {"unsupported error", true}} {
		t.Run(tc.name, func(t *testing.T) {
			rc := newRecheckCounter()
			var app abci.Application = rc
			ba := &batchApp{recheckCounter: rc, reply: func(int) (*abci.ResponseRecheckTxs, error) {
				return nil, abci.ErrRecheckTxsUnsupported
			}}
			if tc.withIface {
				app = ba
			}
			var logs bytes.Buffer
			txmp := newBatchPool(t, app, false, log.NewTMLogger(log.NewSyncWriter(&logs)))
			txs := checkTxs(t, txmp, 10, 0)

			commitTxs(t, txmp, 1, txs[0].tx, txs[1].tx)
			commitTxs(t, txmp, 2, txs[2].tx, txs[3].tx)

			require.True(t, txmp.recheckBatchUnsupported)
			require.Equal(t, 8+6, rc.rechecks, "falls back to per-tx recheck")
			if tc.withIface {
				require.Len(t, ba.batches, 2, "retried each height")
			}
			require.Equal(t, 1, strings.Count(logs.String(), "does not support RecheckTxs"), "logged once")
		})
	}
}

// A multiplexed app that gains RecheckTxs at an upgrade switches to the batch
// path at the next height, and a later fallback is logged again.
func TestTxPool_RecheckBatchUnsupportedThenSupported(t *testing.T) {
	supported := []bool{false, false, true, false}
	app := &batchApp{recheckCounter: newRecheckCounter()}
	app.reply = func(n int) (*abci.ResponseRecheckTxs, error) {
		if !supported[len(app.batches)-1] {
			return nil, abci.ErrRecheckTxsUnsupported
		}
		rsp := &abci.ResponseRecheckTxs{Responses: make([]*abci.ResponseCheckTx, n)}
		for i := range rsp.Responses {
			rsp.Responses[i] = &abci.ResponseCheckTx{Code: abci.CodeTypeOK}
		}
		return rsp, nil
	}
	var logs bytes.Buffer
	txmp := newBatchPool(t, app, false, log.NewTMLogger(log.NewSyncWriter(&logs)))
	txs := checkTxs(t, txmp, 10, 0)

	commitTxs(t, txmp, 1, txs[0].tx, txs[1].tx)
	commitTxs(t, txmp, 2, txs[2].tx, txs[3].tx)
	require.Equal(t, 8+6, app.rechecks)
	commitTxs(t, txmp, 3, txs[4].tx, txs[5].tx)
	require.Equal(t, 8+6, app.rechecks, "height 3 is batched")
	require.Len(t, app.batches[2], 4)
	commitTxs(t, txmp, 4, txs[6].tx, txs[7].tx)

	require.Len(t, app.batches, 4)
	require.Equal(t, 8+6+2, app.rechecks)
	require.Equal(t, 2, txmp.Size())
	require.Equal(t, 2, strings.Count(logs.String(), "does not support RecheckTxs"), "once per transition")
}

// A failed or malformed batch keeps every tx and is not retried per tx, since
// the app may already have changed its check state.
func TestTxPool_RecheckBatchBadReply(t *testing.T) {
	reject := func(n int) *abci.ResponseRecheckTxs {
		rsp := &abci.ResponseRecheckTxs{Responses: make([]*abci.ResponseCheckTx, n)}
		for i := range rsp.Responses {
			rsp.Responses[i] = &abci.ResponseCheckTx{Code: 1}
		}
		return rsp
	}
	for _, tc := range []struct {
		name  string
		reply func(n int) (*abci.ResponseRecheckTxs, error)
	}{
		{"error", func(n int) (*abci.ResponseRecheckTxs, error) { return reject(n), errors.New("boom") }},
		{"nil response", func(int) (*abci.ResponseRecheckTxs, error) { return nil, nil }},
		{"short", func(n int) (*abci.ResponseRecheckTxs, error) { return reject(n - 1), nil }},
		{"long", func(n int) (*abci.ResponseRecheckTxs, error) { return reject(n + 1), nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := &batchApp{recheckCounter: newRecheckCounter(), reply: tc.reply}
			txmp := newBatchPool(t, app, false, log.TestingLogger())
			txs := checkTxs(t, txmp, 10, 0)
			commitTxs(t, txmp, 1, txs[0].tx, txs[1].tx)

			require.Len(t, app.batches, 1)
			require.Zero(t, app.rechecks)
			require.Equal(t, 8, txmp.Size())
			for _, tx := range txs[2:] {
				require.True(t, txmp.Has(tx.tx.Key()))
			}
		})
	}
}
