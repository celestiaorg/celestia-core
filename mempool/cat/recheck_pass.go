package cat

import (
	"math"
	"sync"
	"sync/atomic"
)

// recheckPass lets a proposal reap wait, without txmp.mtx, until the running async recheck
// pass has kept what a reap after the pass would take.
type recheckPass struct {
	quota     [numClasses]int64
	order     []*wrappedTx             // the txs to recheck, in reap order
	kept      [numClasses]atomic.Int64 // messages kept in reapable sender prefixes
	pending   [numClasses]atomic.Int64 // messages the pass has not processed yet
	keptBytes atomic.Int64
	wantBytes atomic.Int64  // the waiting reap's maxBytes
	full      chan struct{} // closed once the waiting reap would be full
	fullOnce  sync.Once
	done      chan struct{} // closed when the pass ends
}

// newRecheckPass snapshots wtxs (in reap order) and counts their messages per class.
func newRecheckPass(quota [numClasses]int64, wtxs []*wrappedTx) *recheckPass {
	p := &recheckPass{quota: quota, order: wtxs, full: make(chan struct{}), done: make(chan struct{})}
	p.wantBytes.Store(math.MaxInt64)
	for _, wtx := range wtxs {
		p.pending[wtx.class].Add(wtx.weight)
	}
	return p
}

// take charges a kept tx in an open sender prefix, which the reap can take, to its class.
func (p *recheckPass) take(wtx *wrappedTx) {
	p.kept[wtx.class].Add(wtx.weight)
	p.keptBytes.Add(wtx.size()) // processed wakes the reap
}

// processed records that the pass is done with wtx, kept or not.
func (p *recheckPass) processed(wtx *wrappedTx) {
	p.pending[wtx.class].Add(-wtx.weight)
	p.wake()
}

// isFull reports whether every class kept its quota or has nothing left to recheck, or the
// kept txs fill the waiting reap's bytes.
func (p *recheckPass) isFull() bool {
	if p.keptBytes.Load() >= p.wantBytes.Load() {
		return true
	}
	for c := range p.quota {
		if p.kept[c].Load() < p.quota[c] && p.pending[c].Load() > 0 {
			return false
		}
	}
	return true
}

func (p *recheckPass) wake() {
	if p.isFull() {
		p.fullOnce.Do(func() { close(p.full) })
	}
}

// wait blocks until a reap of maxBytes (negative: unlimited) would be full or the pass ends.
func (p *recheckPass) wait(maxBytes int64) {
	if maxBytes >= 0 {
		p.wantBytes.Store(maxBytes)
	}
	p.wake()
	select {
	case <-p.full:
	case <-p.done:
	}
}
