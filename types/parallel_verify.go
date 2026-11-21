package types

import (
	"os"
	"runtime"
	"sync"
)

// Sync-speedup prototype: when CELESTIA_PARALLEL_SIGVERIFY=1, light-client commit
// verification (used by the IBC 07-tendermint client) checks all candidate
// signatures concurrently up front, then runs the unchanged sequential tally on
// the precomputed results. The outcome (success / which error) is identical;
// only extra signatures past the 2/3 threshold are also checked.
var parallelSigVerify = os.Getenv("CELESTIA_PARALLEL_SIGVERIFY") == "1"

func parallelVerify(n int, f func(i int) bool) []bool {
	out := make([]bool, n)
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
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				out[i] = f(i)
			}
		}()
	}
	wg.Wait()
	return out
}
