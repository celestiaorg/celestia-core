//go:build !linux

package consensus

import "os"

// openLockedBlockFile is nil without O_DIRECT, which turns the locked-block copy off.
var openLockedBlockFile func(path string) (*os.File, error)
