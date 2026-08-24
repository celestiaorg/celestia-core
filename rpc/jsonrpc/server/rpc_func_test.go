package server

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func dummyRPC() interface{} { return func() {} }

// TestRPCFuncTryAcquireNonHeavy checks that a function without the HeavyFn
// option is never gated: it always admits with a no-op release.
func TestRPCFuncTryAcquireNonHeavy(t *testing.T) {
	f := NewRPCFunc(dummyRPC(), "")
	for i := 0; i < 3; i++ {
		admitted, release := f.tryAcquire()
		require.True(t, admitted)
		require.NotNil(t, release)
		release()
	}
}

// TestRPCFuncTryAcquireHeavyShared checks that heavy functions sharing a
// semaphore draw from a single budget: they admit only up to its capacity,
// reject the rest, and admit again once a slot is released.
func TestRPCFuncTryAcquireHeavyShared(t *testing.T) {
	sem := make(chan struct{}, 1)
	a := NewRPCFunc(dummyRPC(), "", HeavyFn(sem))
	b := NewRPCFunc(dummyRPC(), "", HeavyFn(sem))

	okA, relA := a.tryAcquire()
	require.True(t, okA)

	// b shares a's single slot, so it is rejected until a releases.
	okB, relB := b.tryAcquire()
	require.False(t, okB)
	require.Nil(t, relB)

	relA()
	okB2, relB2 := b.tryAcquire()
	require.True(t, okB2)
	relB2()
}
