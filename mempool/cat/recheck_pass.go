package cat

import "sync/atomic"

// recheckPass is a running async recheck pass. A reap waits on it until every class is safe:
// the rechecked prefix holds cap + margin simple txs of it in single-class simple sender
// prefixes, or no tx the app could still take after the prefix may be of that class.
type recheckPass struct {
	order     []*wrappedTx  // snapshot, in reap order
	done      chan struct{} // closed when the pass ends
	full      chan struct{} // closed once every class is safe
	keptBytes atomic.Int64  // bytes of kept txs in reapable sender prefixes

	// Owned by the pass goroutine.
	early   bool                // the app version's caps are known
	need    [classUnknown]int64 // simple txs that make a class safe
	simple  [classUnknown]int64 // simple txs in simple sender prefixes
	pending [numClasses]int64   // txs left that the app could take
	gone    []bool              // txs the app cannot take, by order index
	safe    [classUnknown]bool  // classes with simple >= need
	open    bool                // the current sender's txs so far are all kept
	chain   int8                // the current sender prefix's class if all simple, else -1
	fullSet bool                // full is closed
}

func newRecheckPass(appVersion uint64, wtxs []*wrappedTx) *recheckPass {
	p := &recheckPass{order: wtxs, done: make(chan struct{}), full: make(chan struct{})}
	if p.need, p.early = classNeeds(appVersion); !p.early {
		return p
	}
	p.gone = make([]bool, len(wtxs))
	// The app fills SDK, then PFB, then PFF txs: a tx behind its sender's tx of a later
	// class fails its sequence check, and so does the rest of its sender's txs.
	for i := range wtxs {
		if i > 0 && sameSender(wtxs[i-1], wtxs[i]) {
			prev := wtxs[i-1]
			p.gone[i] = p.gone[i-1] || prev.class < classUnknown && wtxs[i].class < prev.class
		}
		if !p.gone[i] {
			p.pending[wtxs[i].class]++
		}
	}
	p.check()
	return p
}

func sameSender(a, b *wrappedTx) bool {
	return len(a.sender) > 0 && string(a.sender) == string(b.sender)
}

// processed records that the pass is done with order[i], and whether it kept it.
func (p *recheckPass) processed(i int, kept bool) {
	wtx := p.order[i]
	if i == 0 || !sameSender(p.order[i-1], wtx) {
		p.open, p.chain = true, int8(wtx.class)
	}
	p.open = p.open && kept
	if p.open {
		p.keptBytes.Add(wtx.size())
	}
	if !p.early {
		return
	}
	if !wtx.simple || int8(wtx.class) != p.chain || len(wtx.sender) == 0 {
		p.chain = -1
	}
	if p.gone[i] {
		return
	}
	p.pending[wtx.class]--
	if c := wtx.class; p.open && p.chain >= 0 && !p.safe[c] {
		if p.simple[c]++; p.simple[c] >= p.need[c] {
			p.safe[c] = true
			p.release(c, i+1)
		}
	}
	p.check()
}

// release drops from pending the txs after order[from-1] that sit behind a tx of class c:
// with class c full, the app skips that tx and fails its sender's later ones.
func (p *recheckPass) release(c uint8, from int) {
	behind := false
	for j := from; j < len(p.order); j++ {
		if j == from || !sameSender(p.order[j-1], p.order[j]) {
			behind = false
		}
		if behind && !p.gone[j] {
			p.gone[j] = true
			p.pending[p.order[j].class]--
		}
		behind = behind || p.order[j].class == c
	}
}

// check closes full once every class is safe.
func (p *recheckPass) check() {
	if p.fullSet {
		return
	}
	for c := range p.safe {
		if !p.safe[c] && (p.pending[c] > 0 || p.pending[classUnknown] > 0) {
			return
		}
	}
	p.fullSet = true
	close(p.full)
}

// wait blocks until a reap of maxBytes (negative: unlimited) during the pass gives the app
// no less than one after it, or the pass ends. With the kept txs over half of maxBytes,
// the app's byte limit or the square could bind, which the classes do not model.
func (p *recheckPass) wait(maxBytes int64) {
	select {
	case <-p.done:
	case <-p.full:
		if maxBytes >= 0 && p.keptBytes.Load() > maxBytes/2 {
			<-p.done
		}
	}
}
