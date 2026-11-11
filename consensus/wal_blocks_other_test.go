//go:build !linux

package consensus

// Without O_DIRECT the tests run the locked-block copy through a buffered file.
func init() {
	openLockedBlockFile = openBufferedLockedBlockFile
}
