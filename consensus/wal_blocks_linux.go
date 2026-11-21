package consensus

import (
	"os"
	"syscall"
)

// openLockedBlockFile bypasses the page cache: on ext4 the copy's writeback would stall WAL fsyncs.
var openLockedBlockFile = func(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_DIRECT, 0o600)
}
