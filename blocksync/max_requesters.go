package blocksync

import (
	"os"
	"strconv"
)

const (
	// minMaxRequesters is the smallest usable window: applying block H needs H+1.
	minMaxRequesters = 2
	// maxMaxRequesters caps the window; memory grows with window x block size.
	maxMaxRequesters = 1000
)

// maxRequesters is the number of blocks requested ahead of the one being
// applied. It is read from CELESTIA_BLOCKSYNC_MAX_REQUESTERS and falls back to
// defaultMaxRequesters when unset or out of range.
var maxRequesters = parseMaxRequesters(os.Getenv("CELESTIA_BLOCKSYNC_MAX_REQUESTERS"))

func parseMaxRequesters(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil || n < minMaxRequesters || n > maxMaxRequesters {
		return defaultMaxRequesters
	}
	return n
}
