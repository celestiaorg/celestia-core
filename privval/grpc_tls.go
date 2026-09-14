package privval

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"

	"google.golang.org/grpc/credentials"
)

// GRPCServerCredentials loads TLS transport credentials for the PrivValidator
// gRPC server from PEM files. If clientCAFile is non-empty, mutual TLS is
// required: only clients presenting a certificate signed by that CA are accepted.
func GRPCServerCredentials(certFile, keyFile, clientCAFile string) (credentials.TransportCredentials, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("loading privval gRPC server certificate: %w", err)
	}

	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS13,
	}

	if clientCAFile != "" {
		caPEM, err := os.ReadFile(clientCAFile)
		if err != nil {
			return nil, fmt.Errorf("reading privval gRPC client CA: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caPEM) {
			return nil, fmt.Errorf("no certificates found in privval gRPC client CA file %s", clientCAFile)
		}
		tlsCfg.ClientCAs = pool
		tlsCfg.ClientAuth = tls.RequireAndVerifyClientCert
	}

	return credentials.NewTLS(tlsCfg), nil
}
