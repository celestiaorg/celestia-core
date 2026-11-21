package blocksync

import (
	"os"
	"strconv"
)

// Sync-speedup prototype: CELESTIA_BLOCKSYNC_MAX_REQUESTERS raises the number
// of blocks requested ahead of the one being applied (default 40). With large
// blocks the default window is about one second of blocks, so a single slow
// peer stalls the pipeline. Memory grows with the window: N x block size.
var maxRequesters = func() int {
	if n, err := strconv.Atoi(os.Getenv("CELESTIA_BLOCKSYNC_MAX_REQUESTERS")); err == nil && n > 0 {
		return n
	}
	return defaultMaxRequesters
}()
