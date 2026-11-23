package types

import (
	"runtime"
	"sync"
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

// parallelVerify runs f(i) for i in [0, n) on up to min(GOMAXPROCS, 16) workers.
func parallelVerify(n int, f func(i int) bool) []sigCheck {
	out := make([]sigCheck, n)
	workers := runtime.GOMAXPROCS(0)
	if workers > 16 {
		workers = 16
	}
	if workers > n {
		workers = n
	}
	var wg sync.WaitGroup
	next := make(chan int, n)
	for i := 0; i < n; i++ {
		next <- i
	}
	close(next)
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
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				check(i)
			}
		}()
	}
	wg.Wait()
	return out
}
