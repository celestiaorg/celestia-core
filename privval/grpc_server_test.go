package privval_test

import (
	"context"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/privval"
	privvalproto "github.com/cometbft/cometbft/proto/tendermint/privval"
	"github.com/cometbft/cometbft/types"
)

const testChainID = "test-chain"

func setupGRPCServer(t *testing.T) (privvalproto.PrivValidatorAPIClient, types.PrivValidator) {
	t.Helper()

	pv := types.NewMockPV()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	srv := grpc.NewServer()
	privvalproto.RegisterPrivValidatorAPIServer(srv, privval.NewPrivValidatorGRPCServer(
		pv,
		log.NewNopLogger(),
	))
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient(
		lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	return privvalproto.NewPrivValidatorAPIClient(conn), pv
}

func TestGRPCServerSignRawBytes(t *testing.T) {
	client, pv := setupGRPCServer(t)

	rawBytes := []byte("test commitment data")
	uniqueID := "fiber-commitment"

	resp, err := client.SignRawBytes(context.Background(), &privvalproto.SignRawBytesRequest{
		ChainId:  testChainID,
		RawBytes: rawBytes,
		UniqueId: uniqueID,
	})
	require.NoError(t, err)
	assert.Nil(t, resp.Error)
	assert.NotEmpty(t, resp.Signature)

	// Verify the signature matches what the privval produces directly.
	expectedSig, err := pv.SignRawBytes(testChainID, uniqueID, rawBytes)
	require.NoError(t, err)
	assert.Equal(t, expectedSig, resp.Signature)
}

func TestGRPCServerSignRawBytesError(t *testing.T) {
	pv := types.NewErroringMockPV()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	srv := grpc.NewServer()
	privvalproto.RegisterPrivValidatorAPIServer(srv, privval.NewPrivValidatorGRPCServer(
		pv,
		log.NewNopLogger(),
	))
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient(
		lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	client := privvalproto.NewPrivValidatorAPIClient(conn)
	resp, err := client.SignRawBytes(context.Background(), &privvalproto.SignRawBytesRequest{
		RawBytes: []byte("test data"),
		UniqueId: "fiber-commitment",
	})
	require.NoError(t, err)
	require.NotNil(t, resp.Error)
}
