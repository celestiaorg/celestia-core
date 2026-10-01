// Package parallel runs independent index-keyed work across the available
// cores. It exists so that the hot paths which fan out - erasure coding, block
// part verification, signature verification - share one worker policy instead
// of each growing its own.
package parallel

import (
	"runtime"
	"sync"
)

// For runs fn(i) for every i in [0, n).
//
// The range is split into at most GOMAXPROCS contiguous chunks, and only when
// there is enough work to be worth it: grain is the smallest number of items
// worth handing to a goroutine. fn must be safe to call from several goroutines
// at once. For returns once every call has completed.
func For(n, grain int, fn func(i int)) {
	if n <= 0 {
		return
	}
	if grain < 1 {
		grain = 1
	}

	workers := min(runtime.GOMAXPROCS(0), n/grain)
	if workers <= 1 {
		for i := 0; i < n; i++ {
			fn(i)
		}
		return
	}

	chunk := (n + workers - 1) / workers
	var wg sync.WaitGroup
	for start := 0; start < n; start += chunk {
		end := min(start+chunk, n)
		wg.Add(1)
		go func(start, end int) {
			defer wg.Done()
			for i := start; i < end; i++ {
				fn(i)
			}
		}(start, end)
	}
	wg.Wait()
}
