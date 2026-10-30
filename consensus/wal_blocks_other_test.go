//go:build !linux

package consensus

import "os"

// Without O_DIRECT the tests run the locked-block copy through a buffered file.
func init() {
	openLockedBlockFile = func(path string) (*os.File, error) {
		return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	}
}
