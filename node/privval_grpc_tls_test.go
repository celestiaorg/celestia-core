package node

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	cfg "github.com/cometbft/cometbft/config"
	"github.com/cometbft/cometbft/internal/test"
	"github.com/cometbft/cometbft/libs/log"
	privvalproto "github.com/cometbft/cometbft/proto/tendermint/privval"
)

// TestNodePrivValidatorGRPCPartialTLS checks that Start refuses a partial TLS
// configuration even on loopback with the insecure override, so the signer
// never falls back to plaintext when TLS was intended. Config-level coverage
// of every partial combination lives in config_test.go.
func TestNodePrivValidatorGRPCPartialTLS(t *testing.T) {
	config := test.ResetTestRoot("node_privval_grpc_partial_tls_test")
	defer os.RemoveAll(config.RootDir)
	testFreeConfig(t, config)

	config.PrivValidatorGRPCListenAddr = testFreeAddr(t)
	config.PrivValidatorGRPCAllowInsecure = true
	config.PrivValidatorGRPCCert = "server.crt"

	n, err := DefaultNewNode(config, log.TestingLogger())
	require.NoError(t, err)
	err = n.Start()
	if err == nil {
		_ = n.Stop()
	}
	require.ErrorContains(t, err, "must be set together")
}

// TestNodePrivValidatorGRPCStoppedOnFailedStart checks that the signer is
// shut down when a later startup step fails, since a failed Start never
// reaches OnStop.
func TestNodePrivValidatorGRPCStoppedOnFailedStart(t *testing.T) {
	config := test.ResetTestRoot("node_privval_grpc_failed_start_test")
	defer os.RemoveAll(config.RootDir)
	testFreeConfig(t, config)

	// Occupy the RPC port so Start fails after the signer is up.
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer taken.Close()
	config.RPC.ListenAddress = "tcp://" + taken.Addr().String()

	addr := testFreeAddr(t)
	config.PrivValidatorGRPCListenAddr = addr
	config.PrivValidatorGRPCAllowInsecure = true

	n, err := DefaultNewNode(config, log.TestingLogger())
	require.NoError(t, err)
	err = n.Start()
	if err == nil {
		_ = n.Stop()
	}
	require.True(t, isAddrInUseErr(err), err)

	_, err = net.DialTimeout("tcp", addr, time.Second)
	require.ErrorIs(t, err, syscall.ECONNREFUSED)
}

// TestNodePrivValidatorGRPCMissingTLSFile checks that Start fails when all
// three TLS fields are set but a file cannot be loaded.
func TestNodePrivValidatorGRPCMissingTLSFile(t *testing.T) {
	config := test.ResetTestRoot("node_privval_grpc_missing_tls_file_test")
	defer os.RemoveAll(config.RootDir)
	testFreeConfig(t, config)

	_, certFile, keyFile, _ := test.NewServerCertFiles(t)
	config.PrivValidatorGRPCListenAddr = testFreeAddr(t)
	config.PrivValidatorGRPCCert = certFile
	config.PrivValidatorGRPCKey = keyFile
	config.PrivValidatorGRPCClientCA = filepath.Join(t.TempDir(), "missing-ca.crt")

	n, err := DefaultNewNode(config, log.TestingLogger())
	require.NoError(t, err)
	err = n.Start()
	if err == nil {
		_ = n.Stop()
	}
	require.ErrorContains(t, err, "failed to load privval gRPC TLS credentials")
	require.ErrorIs(t, err, os.ErrNotExist)
}

// TestNodePrivValidatorGRPCMutualTLS checks that a complete TLS configuration
// starts an mTLS signer that serves CA-signed clients and refuses the rest.
func TestNodePrivValidatorGRPCMutualTLS(t *testing.T) {
	config := test.ResetTestRoot("node_privval_grpc_mtls_test")
	defer os.RemoveAll(config.RootDir)

	ca, certFile, keyFile, caFile := test.NewServerCertFiles(t)
	config.PrivValidatorGRPCCert = certFile
	config.PrivValidatorGRPCKey = keyFile
	config.PrivValidatorGRPCClientCA = caFile

	n := startPrivValGRPCNode(t, config)
	defer func() { _ = n.Stop() }()
	addr := n.config.PrivValidatorGRPCListenAddr

	// A client presenting a CA-signed certificate can sign.
	resp, err := signRawBytes(t, addr, n.genesisDoc.ChainID, credentials.NewTLS(ca.ClientTLSConfig(t, true)))
	require.NoError(t, err)
	require.Nil(t, resp.Error)
	require.NotEmpty(t, resp.Signature)

	// A client without a certificate is refused at the TLS handshake.
	err = test.TLSRejection(t, addr, ca.ClientTLSConfig(t, false))
	require.ErrorContains(t, err, "tls: certificate required")

	// A plaintext client never gets a gRPC connection.
	_, err = signRawBytes(t, addr, n.genesisDoc.ChainID, insecure.NewCredentials())
	require.Equal(t, codes.Unavailable, status.Code(err), err)
}

// TestNodePrivValidatorGRPCPlaintextInsecureOverride checks that a signer with
// no TLS files still starts when priv_validator_grpc_allow_insecure is set.
func TestNodePrivValidatorGRPCPlaintextInsecureOverride(t *testing.T) {
	config := test.ResetTestRoot("node_privval_grpc_plaintext_insecure_test")
	defer os.RemoveAll(config.RootDir)
	config.PrivValidatorGRPCAllowInsecure = true

	n := startPrivValGRPCNode(t, config)
	defer func() { _ = n.Stop() }()

	resp, err := signRawBytes(t, n.config.PrivValidatorGRPCListenAddr, n.genesisDoc.ChainID, insecure.NewCredentials())
	require.NoError(t, err)
	require.Nil(t, resp.Error)
	require.NotEmpty(t, resp.Signature)
}

// startPrivValGRPCNode starts a node with a privval gRPC signer on a free
// port, retrying on port conflicts.
func startPrivValGRPCNode(t *testing.T, config *cfg.Config) *Node {
	t.Helper()
	return startNodeRetryingPortConflicts(t, func() (*Node, error) {
		testFreeConfig(t, config)
		config.PrivValidatorGRPCListenAddr = testFreeAddr(t)
		n, err := DefaultNewNode(config, log.TestingLogger())
		if err != nil {
			return nil, err
		}
		return n, n.Start()
	})
}

func signRawBytes(
	t *testing.T,
	addr, chainID string,
	creds credentials.TransportCredentials,
) (*privvalproto.SignedRawBytesResponse, error) {
	t.Helper()
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(creds))
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return privvalproto.NewPrivValidatorAPIClient(conn).SignRawBytes(ctx, &privvalproto.SignRawBytesRequest{
		ChainId:  chainID,
		RawBytes: []byte("fiber commitment payload"),
		UniqueId: "fiber-commitment",
	})
}
