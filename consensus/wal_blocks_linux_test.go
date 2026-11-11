package consensus

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

// shrinkPipe makes f's pipe hold one page, less than a part, whatever the default.
func shrinkPipe(t *testing.T, f *os.File) {
	t.Helper()
	rc, err := f.SyscallConn()
	require.NoError(t, err)
	var errno syscall.Errno
	require.NoError(t, rc.Control(func(fd uintptr) {
		_, _, errno = syscall.Syscall(syscall.SYS_FCNTL, fd, syscall.F_SETPIPE_SZ, uintptr(os.Getpagesize()))
	}))
	if errno != 0 {
		require.NoError(t, errno)
	}
}

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
