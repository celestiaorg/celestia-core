package cat

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	db "github.com/cometbft/cometbft-db"

	"github.com/cometbft/cometbft/abci/example/kvstore"
	abci "github.com/cometbft/cometbft/abci/types"
	"github.com/cometbft/cometbft/config"
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

// batchApp answers RecheckTxs with verdict(tx) for every tx, or with err.
type batchApp struct {
	*recheckCounter
	verdict func(tx []byte) *abci.ResponseCheckTx
	err     error
	batches [][][]byte
}

func (app *batchApp) RecheckTxs(_ context.Context, req *abci.RequestRecheckTxs) (*abci.ResponseRecheckTxs, error) {
	app.batches = append(app.batches, req.Txs)
	if app.err != nil {
		return nil, app.err
	}
	rsp := &abci.ResponseRecheckTxs{Responses: make([]*abci.ResponseCheckTx, len(req.Txs))}
	for i, tx := range req.Txs {
		rsp.Responses[i] = app.verdict(tx)
	}
	return rsp, nil
}

func newRecheckCounter() *recheckCounter {
	return &recheckCounter{application: &application{kvstore.NewApplication(db.NewMemDB())}}
}

func newBatchPool(t *testing.T, app abci.Application, batch bool, logger log.Logger) *TxPool {
	cfg := config.TestMempoolConfig()
	cfg.Recheck = true
	cfg.RecheckBatch = batch
	cli, err := proxy.NewLocalClientCreator(app).NewABCIClient()
	require.NoError(t, err)
	require.NoError(t, cli.Start())
	t.Cleanup(func() {
		os.RemoveAll(cfg.RootDir)
		require.NoError(t, cli.Stop())
	})
	return NewTxPool(logger, cfg, cli, 0)
}

func TestTxPool_RecheckBatch(t *testing.T) {
	app := &batchApp{recheckCounter: newRecheckCounter()}
	txmp := newBatchPool(t, app, true, log.TestingLogger())
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

func TestTxPool_RecheckBatchOff(t *testing.T) {
	app := &batchApp{recheckCounter: newRecheckCounter()}
	txmp := newBatchPool(t, app, false, log.TestingLogger())
	txs := checkTxs(t, txmp, 10, 0)
	commitTxs(t, txmp, 1, txs[0].tx, txs[1].tx)

	require.Empty(t, app.batches)
	require.Equal(t, 8, app.rechecks)
	require.Equal(t, 8, txmp.Size())
}

func TestTxPool_RecheckBatchUnsupported(t *testing.T) {
	var logs bytes.Buffer
	app := newRecheckCounter()
	txmp := newBatchPool(t, app, true, log.NewTMLogger(log.NewSyncWriter(&logs)))
	txs := checkTxs(t, txmp, 10, 0)

	commitTxs(t, txmp, 1, txs[0].tx, txs[1].tx)
	commitTxs(t, txmp, 2, txs[2].tx, txs[3].tx)

	require.Equal(t, 8+6, app.rechecks, "falls back to per-tx recheck")
	require.Equal(t, 1, strings.Count(logs.String(), "does not support RecheckTxs"), "logged once")
}

func TestTxPool_RecheckBatchError(t *testing.T) {
	app := &batchApp{recheckCounter: newRecheckCounter(), err: errors.New("boom")}
	txmp := newBatchPool(t, app, true, log.TestingLogger())
	txs := checkTxs(t, txmp, 10, 0)
	commitTxs(t, txmp, 1, txs[0].tx, txs[1].tx)

	// The app may have changed its check state, so nothing is rechecked again.
	require.Len(t, app.batches, 1)
	require.Zero(t, app.rechecks)
	require.Equal(t, 8, txmp.Size())
}
