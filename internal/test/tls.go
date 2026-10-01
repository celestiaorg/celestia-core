package test

import (
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
)

// CA is an ephemeral certificate authority for TLS tests.
type CA struct {
	Cert *x509.Certificate
	Key  *ecdsa.PrivateKey
	PEM  []byte
}

// NewCA generates a self-signed test CA.
func NewCA(t *testing.T) *CA {
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

	return &CA{
		Cert: cert,
		Key:  key,
		PEM:  pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
	}
}

// Issue creates a leaf certificate signed by the CA and returns PEM-encoded
// cert and key. Server certs are valid for 127.0.0.1.
func (ca *CA) Issue(t *testing.T, cn string, isServer bool) (certPEM, keyPEM []byte) {
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
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.Cert, &key.PublicKey, ca.Key)
	require.NoError(t, err)

	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)

	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM
}

// ClientTLSConfig returns a TLS 1.3 client config trusting the CA, with an
// optional client certificate issued by it.
func (ca *CA) ClientTLSConfig(t *testing.T, withClientCert bool) *tls.Config {
	t.Helper()

	roots := x509.NewCertPool()
	roots.AddCert(ca.Cert)
	cfg := &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS13}
	if withClientCert {
		certPEM, keyPEM := ca.Issue(t, "client", false)
		pair, err := tls.X509KeyPair(certPEM, keyPEM)
		require.NoError(t, err)
		cfg.Certificates = []tls.Certificate{pair}
	}
	return cfg
}

// WriteFile writes data to dir/name and returns the path.
func WriteFile(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, data, 0o600))
	return path
}

// NewServerCertFiles creates a CA, issues a server certificate signed by it,
// and writes the cert, key, and CA cert PEM files to a temp dir.
func NewServerCertFiles(t *testing.T) (ca *CA, certFile, keyFile, caFile string) {
	t.Helper()
	dir := t.TempDir()
	ca = NewCA(t)
	serverCert, serverKey := ca.Issue(t, "server", true)
	certFile = WriteFile(t, dir, "server.crt", serverCert)
	keyFile = WriteFile(t, dir, "server.key", serverKey)
	caFile = WriteFile(t, dir, "ca.crt", ca.PEM)
	return ca, certFile, keyFile, caFile
}

// TLSRejection connects with a raw TLS client and returns the server's
// handshake rejection. In TLS 1.3 the client finishes its handshake before
// the server verifies the client certificate, so the alert only arrives on
// the first read. Reading instead of writing avoids racing the server's close.
func TLSRejection(t *testing.T, addr string, tlsCfg *tls.Config) error {
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
