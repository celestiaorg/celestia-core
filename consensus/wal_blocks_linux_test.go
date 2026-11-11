package consensus

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// Where TMPDIR rejects O_DIRECT, the tests use a buffered file, as on other systems.
func init() {
	dir, err := os.MkdirTemp("", "locked_block_probe")
	if err != nil {
		return
	}
	defer os.RemoveAll(dir)
	f, err := openLockedBlockFile(filepath.Join(dir, "probe"))
	if err == nil {
		_, err = f.Write(alignedBuf(directIOAlign))
		f.Close()
	}
	if errors.Is(err, syscall.EINVAL) {
		openLockedBlockFile = openBufferedLockedBlockFile
	}
}
