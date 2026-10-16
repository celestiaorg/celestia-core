package privval_test

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/privval"
	privvalproto "github.com/cometbft/cometbft/proto/tendermint/privval"
	"github.com/cometbft/cometbft/types"
)

// startLimitedServer starts a PrivValidatorAPI gRPC server on a capped
// listener with the given handshake timeout and returns its address.
func startLimitedServer(t *testing.T, handshakeTimeout time.Duration) string {
	t.Helper()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	srv := grpc.NewServer(grpc.ConnectionTimeout(handshakeTimeout))
	privvalproto.RegisterPrivValidatorAPIServer(srv, privval.NewPrivValidatorGRPCServer(
		types.NewMockPV(),
		testChainID,
		log.NewNopLogger(),
	))
	go func() { _ = srv.Serve(privval.LimitGRPCListener(lis)) }()
	t.Cleanup(srv.Stop)

	return lis.Addr().String()
}

// fillConnectionSlots occupies every connection slot with peers that never
// start a handshake.
func fillConnectionSlots(t *testing.T, addr string) []net.Conn {
	t.Helper()

	stalled := make([]net.Conn, 0, privval.GRPCMaxConnections)
	for i := 0; i < privval.GRPCMaxConnections; i++ {
		c, err := net.Dial("tcp", addr)
		require.NoError(t, err)
		t.Cleanup(func() { _ = c.Close() })
		stalled = append(stalled, c)
	}
	return stalled
}

func newGRPCClient(t *testing.T, addr string) privvalproto.PrivValidatorAPIClient {
	t.Helper()

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return privvalproto.NewPrivValidatorAPIClient(conn)
}

// TestGRPCListenerCapsConnections checks that a client can't connect while
// every slot is occupied. The handshake timeout is far longer than the test
// so no stalled connection can free a slot and mask a missing cap.
func TestGRPCListenerCapsConnections(t *testing.T) {
	addr := startLimitedServer(t, time.Hour)
	fillConnectionSlots(t, addr)
	client := newGRPCClient(t, addr)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_, err := client.GetPubKey(ctx, &privvalproto.PubKeyRequest{ChainId: testChainID}, grpc.WaitForReady(true))
	require.Error(t, err, "connection cap is not enforced")
}

// TestGRPCListenerRecoversFromStalledConnections checks that the server drops
// stalled peers at the handshake deadline, letting a real client through.
func TestGRPCListenerRecoversFromStalledConnections(t *testing.T) {
	addr := startLimitedServer(t, 500*time.Millisecond)
	stalled := fillConnectionSlots(t, addr)
	client := newGRPCClient(t, addr)

	// The client gets a slot once the stalled peers hit the handshake deadline.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := client.GetPubKey(ctx, &privvalproto.PubKeyRequest{ChainId: testChainID}, grpc.WaitForReady(true))
	require.NoError(t, err)
	require.Nil(t, resp.Error)

	// The server closed the stalled sockets, releasing their descriptors:
	// draining ends with EOF rather than a read timeout.
	buf := make([]byte, 1024)
	for _, c := range stalled {
		require.NoError(t, c.SetReadDeadline(time.Now().Add(5*time.Second)))
		err := error(nil)
		for err == nil {
			_, err = c.Read(buf)
		}
		var nerr net.Error
		if errors.As(err, &nerr) && nerr.Timeout() {
			t.Fatal("stalled connection was not closed by the server")
		}
	}
}
