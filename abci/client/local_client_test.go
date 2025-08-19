package abcicli

import (
	"context"
	"testing"

	"github.com/cometbft/cometbft/abci/types"
	cmtsync "github.com/cometbft/cometbft/libs/sync"
)

func TestNewLocalClient(t *testing.T) {
	t.Run("CheckTx should not lock mutex", func(t *testing.T) {
		mutex := new(cmtsync.Mutex)
		app := types.NewBaseApplication()
		client := NewLocalClient(mutex, app)

		mutex.Lock()
		// If CheckTx called Lock on the mutex, the next line would cause a deadlock.
		client.CheckTx(context.Background(), &types.RequestCheckTx{})
		mutex.Unlock()
	})
}
