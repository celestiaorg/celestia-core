package p2p

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"errors"
	"fmt"
	"github.com/gogo/protobuf/proto"
	"github.com/tendermint/tendermint/crypto"
	"github.com/tendermint/tendermint/crypto/encoding"
	crypto2 "github.com/tendermint/tendermint/proto/tendermint/crypto"
	"math/big"
	"time"
)

// TODO(rach-id): mention this code is adapted from libp2p p2p/security/tls/crypto.go

const certValidityPeriod = 24 * time.Hour
const certificatePrefix = "tendermint-tls:"

// TODO(rach-id): update the OID prefix to reflect Celestia/Tendermint
var extensionPrefix = []int{1, 3, 6, 1, 4, 1, 53594}

// getPrefixedExtensionID returns an Object Identifier
// that can be used in x509 Certificates.
func getPrefixedExtensionID(suffix []int) []int {
	return append(extensionPrefix, suffix...)
}

var extensionID = getPrefixedExtensionID([]int{1, 1})
var extensionCritical bool // so we can mark the extension critical in tests

// extensionIDEqual compares two extension IDs.
func extensionIDEqual(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

type signedKey struct {
	PubKey    []byte
	Signature []byte
}

// NewTLSConfig creates a new TLS configuration
func NewTLSConfig(privKey crypto.PrivKey) (*tls.Config, error) {
	template, err := certTemplate()
	if err != nil {
		return nil, err
	}
	cert, err := keyToCertificate(privKey, template)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: true, // This is not insecure here. We will verify the cert chain ourselves.
		ClientAuth:         tls.RequireAnyClientCert,
		Certificates:       []tls.Certificate{*cert},
		VerifyPeerCertificate: func(_ [][]byte, _ [][]*x509.Certificate) error {
			panic("tls config not specialized for peer")
		},
	}, nil
}

// VerifyCertificate verifies the certificate chain and extract the remote's public key.
func VerifyCertificate(cert *x509.Certificate) (crypto.PubKey, error) {
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	var found bool
	var keyExt pkix.Extension
	// find the tendermint key extension, skipping all unknown extensions
	for _, ext := range cert.Extensions {
		if extensionIDEqual(ext.Id, extensionID) {
			keyExt = ext
			found = true
			for i, oident := range cert.UnhandledCriticalExtensions {
				if oident.Equal(ext.Id) {
					// delete the extension from UnhandledCriticalExtensions
					cert.UnhandledCriticalExtensions = append(cert.UnhandledCriticalExtensions[:i], cert.UnhandledCriticalExtensions[i+1:]...)
					break
				}
			}
			break
		}
	}
	if !found {
		return nil, errors.New("expected certificate to contain the key extension")
	}
	if _, err := cert.Verify(x509.VerifyOptions{Roots: pool}); err != nil {
		// If we return an x509 error here, it will be sent on the wire.
		// Wrap the error to avoid that.
		return nil, fmt.Errorf("certificate verification failed: %s", err)
	}

	var sk signedKey
	if _, err := asn1.Unmarshal(keyExt.Value, &sk); err != nil {
		return nil, fmt.Errorf("unmarshalling signed certificate failed: %s", err)
	}
	protoPubKey := crypto2.PublicKey{}
	err := proto.Unmarshal(sk.PubKey, &protoPubKey)
	if err != nil {
		return nil, fmt.Errorf("unmarshalling public key failed: %s", err)
	}
	certKeyPub, err := x509.MarshalPKIXPublicKey(cert.PublicKey)
	if err != nil {
		return nil, err
	}
	pubKey, err := encoding.PubKeyFromProto(protoPubKey)
	valid := pubKey.VerifySignature(append([]byte(certificatePrefix), certKeyPub...), sk.Signature)
	if err != nil {
		return nil, fmt.Errorf("signature verification failed: %s", err)
	}
	if !valid {
		return nil, errors.New("signature invalid")
	}
	return pubKey, nil
}

// GenerateSignedExtension uses the provided private key to sign the public key, and returns the
// signature within a pkix.Extension.
// This extension is included in a certificate to cryptographically tie it to the libp2p private key.
func GenerateSignedExtension(nodePrivateKey crypto.PrivKey, certificatePublicKey *ecdsa.PublicKey) (pkix.Extension, error) {
	protoPubKey, err := encoding.PubKeyToProto(nodePrivateKey.PubKey())
	if err != nil {
		return pkix.Extension{}, err
	}
	keyBytes, err := proto.Marshal(&protoPubKey)
	if err != nil {
		return pkix.Extension{}, err
	}
	certKeyPub, err := x509.MarshalPKIXPublicKey(certificatePublicKey)
	if err != nil {
		return pkix.Extension{}, err
	}
	signature, err := nodePrivateKey.Sign(append([]byte(certificatePrefix), certKeyPub...))
	if err != nil {
		return pkix.Extension{}, err
	}
	value, err := asn1.Marshal(signedKey{
		PubKey:    keyBytes,
		Signature: signature,
	})
	if err != nil {
		return pkix.Extension{}, err
	}

	return pkix.Extension{Id: extensionID, Critical: extensionCritical, Value: value}, nil
}

// keyToCertificate generates a new ECDSA private key and corresponding x509 certificate.
// The certificate includes an extension that cryptographically ties it to the provided libp2p
// private key to authenticate TLS connections.
func keyToCertificate(sk crypto.PrivKey, certTmpl *x509.Certificate) (*tls.Certificate, error) {
	certKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}

	// after calling CreateCertificate, these will end up in Certificate.Extensions
	extension, err := GenerateSignedExtension(sk, &certKey.PublicKey)
	if err != nil {
		return nil, err
	}
	certTmpl.ExtraExtensions = append(certTmpl.ExtraExtensions, extension)

	certDER, err := x509.CreateCertificate(rand.Reader, certTmpl, certTmpl, certKey.Public(), certKey)
	if err != nil {
		return nil, err
	}
	return &tls.Certificate{
		Certificate: [][]byte{certDER},
		PrivateKey:  certKey,
	}, nil
}

// certTemplate returns the template for generating an Identity's TLS certificates.
func certTemplate() (*x509.Certificate, error) {
	bigNum := big.NewInt(1 << 62)
	sn, err := rand.Int(rand.Reader, bigNum)
	if err != nil {
		return nil, err
	}

	subjectSN, err := rand.Int(rand.Reader, bigNum)
	if err != nil {
		return nil, err
	}

	return &x509.Certificate{
		SerialNumber: sn,
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(certValidityPeriod),
		// According to RFC 3280, the issuer field must be set,
		// see https://datatracker.ietf.org/doc/html/rfc3280#section-4.1.2.4.
		Subject: pkix.Name{SerialNumber: subjectSN.String()},
	}, nil
}
