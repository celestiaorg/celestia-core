package privval

import (
	"errors"
	"net"
	"time"

	"github.com/cometbft/cometbft/crypto"
	cmtnet "github.com/cometbft/cometbft/libs/net"
	p2pconn "github.com/cometbft/cometbft/p2p/conn"
)

// Socket errors.
var (
	ErrDialRetryMax = errors.New("dialed maximum retries")
)

// SocketDialer dials a remote address and returns a net.Conn or an error.
type SocketDialer func() (net.Conn, error)

// DialTCPFn dials the given tcp addr, using the given timeoutReadWrite and
// privKey for the authenticated encryption handshake.
func DialTCPFn(addr string, timeoutReadWrite time.Duration, privKey crypto.PrivKey) SocketDialer {
	return func() (net.Conn, error) {
		conn, err := cmtnet.Connect(addr)
		if err != nil {
			return nil, err
		}
		deadline := time.Now().Add(timeoutReadWrite)
		if err := conn.SetDeadline(deadline); err != nil {
			_ = conn.Close()
			return nil, err
		}
		secretConn, err := p2pconn.MakeSecretConnection(conn, privKey)
		if err != nil {
			// Don't leak the connection when the handshake fails.
			_ = conn.Close()
			return nil, err
		}
		return secretConn, nil
	}
}

// DialUnixFn dials the given unix socket.
func DialUnixFn(addr string) SocketDialer {
	return func() (net.Conn, error) {
		unixAddr := &net.UnixAddr{Name: addr, Net: "unix"}
		return net.DialUnix("unix", nil, unixAddr)
	}
}
