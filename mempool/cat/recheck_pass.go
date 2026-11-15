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
	kept      [numClasses]atomic.Int64 // messages kept in reapable sender prefixes, charged as the reap does
	pending   [numClasses]atomic.Int64 // messages the pass has not processed yet; valid once ready
	ready     atomic.Bool
	keptBytes atomic.Int64
	wantBytes atomic.Int64  // the waiting reap's maxBytes
	full      chan struct{} // closed once the waiting reap would be full
	fullOnce  sync.Once
	done      chan struct{} // closed when the pass ends
}

func newRecheckPass(quota [numClasses]int64) *recheckPass {
	p := &recheckPass{quota: quota, full: make(chan struct{}), done: make(chan struct{})}
	p.wantBytes.Store(math.MaxInt64)
	return p
}

// start counts the messages of the pass's snapshot, in the order it rechecks them.
func (p *recheckPass) start(wtxs []*wrappedTx) {
	for _, wtx := range wtxs {
		p.pending[wtx.class].Add(wtx.weight)
	}
	p.ready.Store(true)
	p.wake()
}

// take charges a kept tx in an open sender prefix to its class, as the reap will; false if
// the class is full, which ends the sender's reapable prefix.
func (p *recheckPass) take(wtx *wrappedTx) bool {
	if p.kept[wtx.class].Load()+wtx.weight > p.quota[wtx.class] {
		return false
	}
	p.kept[wtx.class].Add(wtx.weight)
	p.keptBytes.Add(wtx.size())
	p.wake()
	return true
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
	if !p.ready.Load() {
		return false
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

// candidatesFirst moves the signer sets holding the next reap's candidates (each class's
// first quota messages in reap order, if all are kept) ahead of the others. Sets move whole,
// in reap order, so each sender is still rechecked in sequence order.
func candidatesFirst(wtxs []*wrappedTx, quota [numClasses]int64) []*wrappedTx {
	var used [numClasses]int64
	first := make([]*wrappedTx, 0, len(wtxs))
	var rest []*wrappedTx
	for i := 0; i < len(wtxs); {
		j := i + 1
		for len(wtxs[i].sender) > 0 && j < len(wtxs) && string(wtxs[j].sender) == string(wtxs[i].sender) {
			j++
		}
		candidate := false
		for _, wtx := range wtxs[i:j] {
			if used[wtx.class]+wtx.weight > quota[wtx.class] {
				break
			}
			used[wtx.class] += wtx.weight
			candidate = true
		}
		if candidate {
			first = append(first, wtxs[i:j]...)
		} else {
			rest = append(rest, wtxs[i:j]...)
		}
		i = j
	}
	return append(first, rest...)
}
