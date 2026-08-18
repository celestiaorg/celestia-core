package coregrpc

import (
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	heavyMethod = "/tendermint.rpc.grpc.BlockAPI/BlockByHeight"
	lightMethod = "/tendermint.rpc.grpc.BlockAPI/Status"
	subMethod   = "/tendermint.rpc.grpc.BlockAPI/SubscribeNewHeights"
)

// TestAcquireHeavyFnNilSemaphore verifies the limit-disabled case: a nil
// semaphore always admits, even for heavy methods, with a no-op release.
func TestAcquireHeavyFnNilSemaphore(t *testing.T) {
	release, ok := acquireHeavyFn(nil, heavyMethod)
	require.True(t, ok)
	require.NotNil(t, release)
	release()
}

// TestAcquireHeavyFnLightMethodNotGated verifies that non-heavy methods (and the
// long-lived subscription stream) never draw from the budget: they admit even
// when the semaphore is full.
func TestAcquireHeavyFnLightMethodNotGated(t *testing.T) {
	sem := make(chan struct{}, 1)
	sem <- struct{}{} // saturate

	for _, method := range []string{lightMethod, subMethod} {
		release, ok := acquireHeavyFn(sem, method)
		require.True(t, ok, "method %s should not be gated", method)
		require.NotNil(t, release)
		release()
	}
	require.Len(t, sem, 1, "light methods must not touch the budget")
}

// TestAcquireHeavyFnGatedAndReleased verifies that heavy methods draw from the
// shared budget: they admit up to capacity, reject the rest, and admit again
// once a slot is released.
func TestAcquireHeavyFnGatedAndReleased(t *testing.T) {
	sem := make(chan struct{}, 1)

	release, ok := acquireHeavyFn(sem, heavyMethod)
	require.True(t, ok)

	_, ok2 := acquireHeavyFn(sem, heavyMethod)
	require.False(t, ok2, "second heavy request should be rejected when full")

	release()
	release3, ok3 := acquireHeavyFn(sem, heavyMethod)
	require.True(t, ok3, "slot should be free again after release")
	release3()
}
