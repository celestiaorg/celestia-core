package abcicli

import (
	"context"
	"testing"

	"github.com/cometbft/cometbft/abci/types"
	cmtsync "github.com/cometbft/cometbft/libs/sync"
	"github.com/cometbft/cometbft/libs/trace"
	"github.com/stretchr/testify/require"
)

func TestNewLocalClient(t *testing.T) {
	t.Run("CheckTx should not lock mutex", func(t *testing.T) {
		mutex := new(cmtsync.Mutex)
		app := types.NewBaseApplication()
		traceClient := trace.NoOpTracer()
		client := NewLocalClient(mutex, app, traceClient)

		mutex.Lock()
		// If CheckTx called Lock on the mutex, the next line would cause a deadlock.
		_, err := client.CheckTx(context.Background(), &types.RequestCheckTx{})
		require.NoError(t, err)
		mutex.Unlock()
	})
	t.Run("CheckTxAsync should not lock mutex", func(t *testing.T) {
		mutex := new(cmtsync.Mutex)
		app := types.NewBaseApplication()
		traceClient := trace.NoOpTracer()
		client := NewLocalClient(mutex, app, traceClient)
		client.SetResponseCallback(mockCallback())

		mutex.Lock()
		// If CheckTxAsync called Lock on the mutex, the next line would cause a deadlock.
		_, err := client.CheckTxAsync(context.Background(), &types.RequestCheckTx{})
		require.NoError(t, err)
		mutex.Unlock()
	})
}

func mockCallback() func(*types.Request, *types.Response) {
	return func(req *types.Request, res *types.Response) {}
}
