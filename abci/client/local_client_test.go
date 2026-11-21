package abcicli_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	abcicli "github.com/cometbft/cometbft/abci/client"
	"github.com/cometbft/cometbft/abci/example/kvstore"
	"github.com/cometbft/cometbft/abci/types"
)

type recheckTxsApp struct {
	*kvstore.Application
	onRecheck func()
}

func (app *recheckTxsApp) RecheckTxs(_ context.Context, req *types.RequestRecheckTxs) (*types.ResponseRecheckTxs, error) {
	app.onRecheck()
	rsp := &types.ResponseRecheckTxs{Responses: make([]*types.ResponseCheckTx, len(req.Txs))}
	for i := range req.Txs {
		rsp.Responses[i] = &types.ResponseCheckTx{Code: uint32(i)}
	}
	return rsp, nil
}

func TestLocalClientRecheckTxs(t *testing.T) {
	ctx := context.Background()
	req := &types.RequestRecheckTxs{Txs: [][]byte{[]byte("a"), []byte("b")}}

	plain := abcicli.NewLocalClient(nil, kvstore.NewInMemoryApplication())
	_, err := plain.(types.RecheckTxsApplication).RecheckTxs(ctx, req)
	require.ErrorIs(t, err, types.ErrRecheckTxsUnsupported)

	app := &recheckTxsApp{Application: kvstore.NewInMemoryApplication()}
	cli := abcicli.NewLocalClient(nil, app)
	// The batch holds the client mutex, as each per-tx CheckTx would.
	app.onRecheck = func() {
		done := make(chan struct{})
		go func() {
			_, _ = cli.Info(ctx, &types.RequestInfo{})
			close(done)
		}()
		select {
		case <-done:
			t.Error("Info ran while RecheckTxs held the client")
		case <-time.After(50 * time.Millisecond):
		}
	}
	rsp, err := cli.(types.RecheckTxsApplication).RecheckTxs(ctx, req)
	require.NoError(t, err)
	require.Len(t, rsp.Responses, 2)
	require.EqualValues(t, 1, rsp.Responses[1].Code)
}
