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

// TestGRPCListenerRecoversFromStalledConnections fills every connection slot
// with peers that never start a handshake and checks that the server drops
// them at the handshake deadline, letting a real client through.
func TestGRPCListenerRecoversFromStalledConnections(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	srv := grpc.NewServer(grpc.ConnectionTimeout(2 * time.Second))
	privvalproto.RegisterPrivValidatorAPIServer(srv, privval.NewPrivValidatorGRPCServer(
		types.NewMockPV(),
		testChainID,
		log.NewNopLogger(),
	))
	go func() { _ = srv.Serve(privval.LimitGRPCListener(lis)) }()
	t.Cleanup(srv.Stop)

	stalled := make([]net.Conn, 0, privval.GRPCMaxConnections)
	for i := 0; i < privval.GRPCMaxConnections; i++ {
		c, err := net.Dial("tcp", lis.Addr().String())
		require.NoError(t, err)
		t.Cleanup(func() { _ = c.Close() })
		stalled = append(stalled, c)
	}

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	client := privvalproto.NewPrivValidatorAPIClient(conn)

	// While every slot is occupied, the cap holds a 17th connection back.
	cappedCtx, cappedCancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cappedCancel()
	_, err = client.GetPubKey(cappedCtx, &privvalproto.PubKeyRequest{ChainId: testChainID}, grpc.WaitForReady(true))
	require.Error(t, err, "connection cap is not enforced")

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
