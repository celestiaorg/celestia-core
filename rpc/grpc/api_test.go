package coregrpc

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/rpc/core"
)

// TestStop_DoesNotDeadlock is a regression test for
// https://github.com/celestiaorg/celestia-core/issues/3003.
// Stop acquires blockAPI.Lock() and then calls closeAllListeners which
// also tries to acquire the same non-reentrant mutex, causing the
// gRPC server shutdown path to hang indefinitely.
func TestStop_DoesNotDeadlock(t *testing.T) {
	env := &core.Environment{Logger: log.NewNopLogger()}
	api := NewBlockAPI(env)

	// Register a couple of listeners so closeAllListeners has work to do.
	_ = api.addHeightListener()
	_ = api.addHeightListener()

	done := make(chan error, 1)
	go func() {
		done <- api.Stop(context.Background())
	}()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() deadlocked: did not return within 2s")
	}
}
