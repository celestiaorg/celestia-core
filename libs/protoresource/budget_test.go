package protoresource_test

import (
	"math"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/libs/protoresource"
)

func TestBudget(t *testing.T) {
	b := protoresource.NewBudget(10)
	r, ok := b.TryAcquire(6)
	require.True(t, ok, "reservation refused")
	_, ok = b.TryAcquire(5)
	require.False(t, ok, "capacity exceeded")
	_, ok = b.TryAcquire(math.MaxUint64)
	require.False(t, ok, "overflow admitted")
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() { r.Release() })
	}
	wg.Wait()
	require.Zero(t, b.Used(), "reservation leaked")
	r, ok = b.TryAcquire(10)
	require.True(t, ok, "capacity not restored")
	r.Release()
	var empty protoresource.Budget
	_, ok = empty.TryAcquire(1)
	require.False(t, ok, "zero budget admitted work")
}
