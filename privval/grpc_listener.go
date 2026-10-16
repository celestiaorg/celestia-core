package privval

import (
	"net"
	"time"

	"golang.org/x/net/netutil"
)

const (
	// GRPCMaxConnections caps concurrent connections to the privval gRPC
	// server. The endpoint serves a handful of trusted clients, so a peer
	// holding sockets open can't exhaust the process's file descriptors.
	GRPCMaxConnections = 16

	// GRPCConnectionTimeout bounds how long a new connection may take to
	// finish its TLS and HTTP/2 handshake before its socket is closed.
	GRPCConnectionTimeout = 10 * time.Second
)

// LimitGRPCListener caps the number of connections the privval gRPC server
// holds concurrently at GRPCMaxConnections.
func LimitGRPCListener(lis net.Listener) net.Listener {
	return netutil.LimitListener(lis, GRPCMaxConnections)
}
