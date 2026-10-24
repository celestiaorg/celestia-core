package protoresource

import "sync/atomic"

// Budget bounds simultaneously retained bytes or allocation units.
// A zero-value Budget admits no work. It never blocks a receive routine.
type Budget struct {
	capacity uint64
	used     atomic.Uint64
}

// NewBudget creates a fixed-capacity process or peer budget.
func NewBudget(capacity uint64) *Budget { return &Budget{capacity: capacity} }

// Reservation remains charged until its owner releases all retained data.
type Reservation struct {
	budget   *Budget
	weight   uint64
	released atomic.Bool
}

// TryAcquire reserves weight, or reports overload before expensive work starts.
func (b *Budget) TryAcquire(weight uint64) (*Reservation, bool) {
	for {
		used := b.used.Load()
		if weight > b.capacity-used {
			return nil, false
		}
		if b.used.CompareAndSwap(used, used+weight) {
			return &Reservation{budget: b, weight: weight}, true
		}
	}
}

// Release is safe on a nil reservation and idempotent across concurrent exits.
func (r *Reservation) Release() {
	if r != nil && r.released.CompareAndSwap(false, true) {
		r.budget.used.Add(-r.weight)
	}
}

// Used returns the current reserved weight.
func (b *Budget) Used() uint64 { return b.used.Load() }
