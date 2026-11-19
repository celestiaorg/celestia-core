package privval

import (
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/crypto/ed25519"
)

func getDialerTestCases(t *testing.T) []dialerTestCase {
	tcpAddr := GetFreeLocalhostAddrPort()
	unixFilePath, err := testUnixAddr()
	require.NoError(t, err)
	unixAddr := fmt.Sprintf("unix://%s", unixFilePath)

	return []dialerTestCase{
		{
			addr:   tcpAddr,
			dialer: DialTCPFn(tcpAddr, testTimeoutReadWrite, ed25519.GenPrivKey()),
		},
		{
			addr:   unixAddr,
			dialer: DialUnixFn(unixFilePath),
		},
	}
}

func TestIsConnTimeoutForFundamentalTimeouts(t *testing.T) {
	// Generate a networking timeout
	tcpAddr := GetFreeLocalhostAddrPort()
	dialer := DialTCPFn(tcpAddr, time.Millisecond, ed25519.GenPrivKey())
	_, err := dialer()
	assert.Error(t, err)
	assert.True(t, IsConnTimeout(err))
}

func TestIsConnTimeoutForWrappedConnTimeouts(t *testing.T) {
	tcpAddr := GetFreeLocalhostAddrPort()
	dialer := DialTCPFn(tcpAddr, time.Millisecond, ed25519.GenPrivKey())
	_, err := dialer()
	assert.Error(t, err)
	err = fmt.Errorf("%v: %w", err, ErrConnectionTimeout)
	assert.True(t, IsConnTimeout(err))
}

// A failed handshake must close the connection, so repeated dials don't leak sockets.
func TestDialTCPFnClosesConnOnFailedHandshake(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()

	// The remote side accepts the connection but never answers the handshake.
	closed := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			closed <- err
			return
		}
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		// Returns nil once the dialer closes its end.
		_, err = io.Copy(io.Discard, conn)
		closed <- err
	}()

	conn, err := DialTCPFn(ln.Addr().String(), 50*time.Millisecond, ed25519.GenPrivKey())()
	require.Error(t, err)
	require.Nil(t, conn)
	require.NoError(t, <-closed)
}
