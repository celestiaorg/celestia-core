package privval_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/privval"
	privvalproto "github.com/cometbft/cometbft/proto/tendermint/privval"
	"github.com/cometbft/cometbft/types"
)

// testCA is an ephemeral certificate authority for TLS tests.
type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte
}

func newTestCA(t *testing.T) *testCA {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)

	return &testCA{
		cert: cert,
		key:  key,
		pem:  pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
	}
}

// issue creates a leaf certificate signed by the CA and returns PEM-encoded
// cert and key.
func (ca *testCA) issue(t *testing.T, cn string, isServer bool) (certPEM, keyPEM []byte) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	if isServer {
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		tmpl.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
	} else {
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	require.NoError(t, err)

	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)

	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM
}

func writeFile(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, data, 0o600))
	return path
}

// newServerCertFiles creates a CA, issues a server certificate signed by it,
// and writes the cert, key, and CA cert PEM files to a temp dir.
func newServerCertFiles(t *testing.T) (ca *testCA, certFile, keyFile, caFile string) {
	t.Helper()
	dir := t.TempDir()
	ca = newTestCA(t)
	serverCert, serverKey := ca.issue(t, "privval-server", true)
	certFile = writeFile(t, dir, "server.crt", serverCert)
	keyFile = writeFile(t, dir, "server.key", serverKey)
	caFile = writeFile(t, dir, "ca.crt", ca.pem)
	return ca, certFile, keyFile, caFile
}

// startTLSServer starts a PrivValidatorAPI gRPC server with the given
// credentials and returns its address.
func startTLSServer(t *testing.T, creds credentials.TransportCredentials) string {
	t.Helper()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	srv := grpc.NewServer(grpc.Creds(creds))
	privvalproto.RegisterPrivValidatorAPIServer(srv, privval.NewPrivValidatorGRPCServer(
		types.NewMockPV(),
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

// tlsRejection connects with a raw TLS client and returns the server's
// handshake rejection. In TLS 1.3 the client finishes its handshake before
// the server verifies the client certificate, so the alert only arrives on
// the first read. Reading instead of writing avoids racing the server's close.
func tlsRejection(t *testing.T, addr string, tlsCfg *tls.Config) error {
	t.Helper()

	conn, err := tls.Dial("tcp", addr, tlsCfg)
	if err != nil {
		return err
	}
	defer conn.Close()

	require.NoError(t, conn.SetReadDeadline(time.Now().Add(5*time.Second)))
	_, err = conn.Read(make([]byte, 1))
	return err
}

func TestGRPCServerCredentialsMutualTLS(t *testing.T) {
	ca, certFile, keyFile, caFile := newServerCertFiles(t)

	creds, err := privval.GRPCServerCredentials(certFile, keyFile, caFile)
	require.NoError(t, err)
	addr := startTLSServer(t, creds)

	rootPool := x509.NewCertPool()
	require.True(t, rootPool.AppendCertsFromPEM(ca.pem))

	clientCert, clientKey := ca.issue(t, "privval-client", false)
	clientPair, err := tls.X509KeyPair(clientCert, clientKey)
	require.NoError(t, err)

	// A client presenting a certificate signed by the CA succeeds.
	err = getPubKey(addr, &tls.Config{
		RootCAs:      rootPool,
		Certificates: []tls.Certificate{clientPair},
		MinVersion:   tls.VersionTLS13,
	})
	require.NoError(t, err)

	// A client without a certificate is rejected.
	err = tlsRejection(t, addr, &tls.Config{
		RootCAs:    rootPool,
		MinVersion: tls.VersionTLS13,
	})
	require.ErrorContains(t, err, "tls: certificate required")

	// A client with a certificate from a different CA is rejected.
	otherCA := newTestCA(t)
	otherCert, otherKey := otherCA.issue(t, "impostor", false)
	otherPair, err := tls.X509KeyPair(otherCert, otherKey)
	require.NoError(t, err)
	err = tlsRejection(t, addr, &tls.Config{
		RootCAs:      rootPool,
		Certificates: []tls.Certificate{otherPair},
		MinVersion:   tls.VersionTLS13,
	})
	require.ErrorContains(t, err, "tls: unknown certificate authority")
}

func TestGRPCServerCredentialsErrors(t *testing.T) {
	_, certFile, keyFile, caFile := newServerCertFiles(t)
	dir := t.TempDir()

	_, err := privval.GRPCServerCredentials(filepath.Join(dir, "missing.crt"), keyFile, caFile)
	require.ErrorIs(t, err, os.ErrNotExist)
	require.ErrorContains(t, err, "loading privval gRPC server certificate")

	_, err = privval.GRPCServerCredentials(certFile, keyFile, filepath.Join(dir, "missing-ca.crt"))
	require.ErrorIs(t, err, os.ErrNotExist)
	require.ErrorContains(t, err, "reading privval gRPC client CA")

	badCA := writeFile(t, dir, "bad-ca.crt", []byte("not a pem"))
	_, err = privval.GRPCServerCredentials(certFile, keyFile, badCA)
	require.ErrorContains(t, err, "no certificates found in privval gRPC client CA file")
}
