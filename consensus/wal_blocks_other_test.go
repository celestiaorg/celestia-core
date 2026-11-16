//go:build !linux

package consensus

import (
	"os"
	"testing"
)

// Without O_DIRECT the tests run the locked-block copy through a buffered file.
func init() {
	openLockedBlockFile = openBufferedLockedBlockFile
}

// shrinkPipe is a no-op: pipes here hold at most 64 KiB, less than one part record.
func shrinkPipe(*testing.T, *os.File) {}
