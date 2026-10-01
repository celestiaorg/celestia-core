package privval_test

import (
	"context"
	"crypto/tls"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/cometbft/cometbft/internal/test"
	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/privval"
	privvalproto "github.com/cometbft/cometbft/proto/tendermint/privval"
	"github.com/cometbft/cometbft/types"
)

// startTLSServer starts a PrivValidatorAPI gRPC server with the given
// credentials and returns its address.
func startTLSServer(t *testing.T, creds credentials.TransportCredentials) string {
	t.Helper()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	srv := grpc.NewServer(grpc.Creds(creds))
	privvalproto.RegisterPrivValidatorAPIServer(srv, privval.NewPrivValidatorGRPCServer(
		types.NewMockPV(),
		testChainID,
		log.NewNopLogger(),
	))
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	return lis.Addr().String()
}

func getPubKey(addr string, tlsCfg *tls.Config) error {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)))
	if err != nil {
		return err
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = privvalproto.NewPrivValidatorAPIClient(conn).GetPubKey(ctx, &privvalproto.PubKeyRequest{ChainId: testChainID})
	return err
}

func TestGRPCServerCredentialsMutualTLS(t *testing.T) {
	ca, certFile, keyFile, caFile := test.NewServerCertFiles(t)

	creds, err := privval.GRPCServerCredentials(certFile, keyFile, caFile)
	require.NoError(t, err)
	addr := startTLSServer(t, creds)

	// A client presenting a certificate signed by the CA succeeds.
	require.NoError(t, getPubKey(addr, ca.ClientTLSConfig(t, true)))

	// A client without a certificate is rejected.
	err = test.TLSRejection(t, addr, ca.ClientTLSConfig(t, false))
	require.ErrorContains(t, err, "tls: certificate required")

	// A client with a certificate from a different CA is rejected.
	impostor := test.NewCA(t).ClientTLSConfig(t, true)
	impostor.RootCAs = ca.ClientTLSConfig(t, false).RootCAs
	err = test.TLSRejection(t, addr, impostor)
	require.ErrorContains(t, err, "tls: unknown certificate authority")
}

func TestGRPCServerCredentialsErrors(t *testing.T) {
	_, certFile, keyFile, caFile := test.NewServerCertFiles(t)
	dir := t.TempDir()

	_, err := privval.GRPCServerCredentials(filepath.Join(dir, "missing.crt"), keyFile, caFile)
	require.ErrorIs(t, err, os.ErrNotExist)
	require.ErrorContains(t, err, "loading privval gRPC server certificate")

	_, err = privval.GRPCServerCredentials(certFile, keyFile, filepath.Join(dir, "missing-ca.crt"))
	require.ErrorIs(t, err, os.ErrNotExist)
	require.ErrorContains(t, err, "reading privval gRPC client CA")

	badCA := test.WriteFile(t, dir, "bad-ca.crt", []byte("not a pem"))
	_, err = privval.GRPCServerCredentials(certFile, keyFile, badCA)
	require.ErrorContains(t, err, "no certificates found in privval gRPC client CA file")
}
