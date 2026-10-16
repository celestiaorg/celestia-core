package protoresource_test

import (
	"math"
	"sync"
	"testing"

	"github.com/cometbft/cometbft/libs/protoresource"
)

func TestBudget(t *testing.T) {
	b := protoresource.NewBudget(10)
	r, ok := b.TryAcquire(6)
	if !ok {
		t.Fatal("reservation refused")
	}
	if _, ok := b.TryAcquire(5); ok {
		t.Fatal("capacity exceeded")
	}
	if _, ok := b.TryAcquire(math.MaxUint64); ok {
		t.Fatal("overflow admitted")
	}
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() { r.Release() })
	}
	wg.Wait()
	if b.Used() != 0 {
		t.Fatalf("reservation leaked: %d", b.Used())
	}
	r, ok = b.TryAcquire(10)
	if !ok {
		t.Fatal("capacity not restored")
	}
	r.Release()
	var empty protoresource.Budget
	if _, ok := empty.TryAcquire(1); ok {
		t.Fatal("zero budget admitted work")
	}
}
