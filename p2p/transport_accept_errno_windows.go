//go:build windows

package p2p

import "syscall"

// Winsock reports resource exhaustion with its own error codes. On Windows,
// syscall.EMFILE and syscall.ENOBUFS are invented values that do not match
// them, and syscall.Errno.Temporary does not recognize them.
const (
	wsaEMFILE  syscall.Errno = 10024 // WSAEMFILE: too many open sockets
	wsaENOBUFS syscall.Errno = 10055 // WSAENOBUFS: no buffer space available
)

// platformTemporaryAcceptErrnos lists the extra Accept errors that are
// temporary on this platform.
var platformTemporaryAcceptErrnos = []syscall.Errno{wsaEMFILE, wsaENOBUFS}
