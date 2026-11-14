//go:build windows

package p2p

import (
	"net"
	"os"
	"syscall"
	"testing"
)

func TestIsTemporaryAcceptErrorWinsock(t *testing.T) {
	wrap := func(errno syscall.Errno) error {
		return &net.OpError{Op: "accept", Net: "tcp", Err: os.NewSyscallError("acceptex", errno)}
	}
	testCases := []struct {
		name  string
		errno syscall.Errno
		want  bool
	}{
		{"WSAEMFILE", wsaEMFILE, true},
		{"WSAENOBUFS", wsaENOBUFS, true},
		{"WSAECONNRESET", syscall.WSAECONNRESET, false},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isTemporaryAcceptError(wrap(tc.errno)); got != tc.want {
				t.Fatalf("isTemporaryAcceptError(%v) = %v, want %v", tc.errno, got, tc.want)
			}
		})
	}
}
