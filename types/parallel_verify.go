package types

import (
	"runtime"
	"sync"
	"sync/atomic"
)

// Light-client commit verification (VerifyCommitLight*, used by the IBC
// 07-tendermint client and blocksync) checks all candidate signatures
// concurrently up front, then runs the unchanged sequential tally on the
// precomputed results. The outcome (success / which error / panic) is
// identical; only signatures past the 2/3 threshold are also checked.

// parallelSigVerify is only switched off by tests, to compare with the
// sequential path.
var parallelSigVerify = true

// sigCheck is the result of a precomputed signature check.
type sigCheck uint8

const (
	// sigUnchecked means the check panicked on a worker goroutine; the caller
	// must redo it on its own goroutine so a panic surfaces exactly as it
	// would without the pre-pass.
	sigUnchecked sigCheck = iota
	sigValid
	sigInvalid
)

// minParallelSigs is the fewest scheduled signatures worth spreading over
// goroutines; below it the sequential loop verifies them itself.
const minParallelSigs = 8

// parallelVerify runs f(i) for each i in idxs on up to min(GOMAXPROCS, 16)
// workers and returns the results indexed by i in [0, n). Indices that are not
// scheduled, or whose check panicked, stay sigUnchecked. It returns nil when
// fewer than minParallelSigs indices are scheduled.
func parallelVerify(n int, idxs []int, f func(i int) bool) []sigCheck {
	if len(idxs) < minParallelSigs {
		return nil
	}
	out := make([]sigCheck, n)
	workers := runtime.GOMAXPROCS(0)
	if workers > 16 {
		workers = 16
	}
	if workers > len(idxs) {
		workers = len(idxs)
	}
	check := func(i int) {
		defer func() {
			if recover() != nil {
				out[i] = sigUnchecked
			}
		}()
		if f(i) {
			out[i] = sigValid
		} else {
			out[i] = sigInvalid
		}
	}
	var (
		wg   sync.WaitGroup
		next atomic.Int64
	)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				k := int(next.Add(1) - 1)
				if k >= len(idxs) {
					return
				}
				check(idxs[k])
			}
		}()
	}
	wg.Wait()
	return out
}
