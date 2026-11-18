//go:build !windows

package p2p

import "syscall"

// platformTemporaryAcceptErrnos lists the extra Accept errors that are
// temporary on this platform. The POSIX errnos are checked on all platforms.
var platformTemporaryAcceptErrnos []syscall.Errno
