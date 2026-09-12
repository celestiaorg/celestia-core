package server

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func dummyRPC() interface{} { return func() {} }

// TestRPCFuncHeavyCapacityAndRelease checks that several heavy functions sharing
// a budget of capacity > 1 admit up to that capacity, reject the rest, and that
// a single release frees exactly one slot.
func TestRPCFuncHeavyCapacityAndRelease(t *testing.T) {
	sem := make(chan struct{}, 2)
	a := NewRPCFunc(dummyRPC(), "", HeavyFn(sem))
	b := NewRPCFunc(dummyRPC(), "", HeavyFn(sem))
	c := NewRPCFunc(dummyRPC(), "", HeavyFn(sem))

	okA, relA := a.tryAcquire()
	require.True(t, okA)
	okB, relB := b.tryAcquire()
	require.True(t, okB)

	// Budget of 2 is full: the third heavy function is rejected.
	okC, relC := c.tryAcquire()
	require.False(t, okC)
	require.Nil(t, relC)

	// One release frees exactly one slot, admitting c.
	relA()
	okC2, relC2 := c.tryAcquire()
	require.True(t, okC2)

	relB()
	relC2()
	require.Len(t, sem, 0, "every slot must be released")
}

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
