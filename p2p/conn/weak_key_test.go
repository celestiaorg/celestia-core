package conn

import (
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/crypto"
	"github.com/cometbft/cometbft/crypto/ed25519"
)

// An identity key can produce this signature for any challenge without a secret.
type identityPrivateKey struct{ crypto.PrivKey }

func (identityPrivateKey) PubKey() crypto.PubKey {
	key := make(ed25519.PubKey, ed25519.PubKeySize)
	key[0] = 1
	return key
}

func (identityPrivateKey) Sign([]byte) ([]byte, error) {
	sig := make([]byte, ed25519.SignatureSize)
	sig[0] = 1
	return sig, nil
}

func TestSecretConnectionRejectsIdentityKey(t *testing.T) {
	forger := identityPrivateKey{ed25519.GenPrivKey()}
	sig, err := forger.Sign(nil)
	require.NoError(t, err)
	// Keep historical ZIP-215 signature semantics separate from admission.
	require.True(t, forger.PubKey().VerifySignature([]byte("any challenge"), sig))

	honest, attacker := net.Pipe()
	t.Cleanup(func() { _ = honest.Close(); _ = attacker.Close() })
	require.NoError(t, honest.SetDeadline(time.Now().Add(5*time.Second)))
	require.NoError(t, attacker.SetDeadline(time.Now().Add(5*time.Second)))
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = MakeSecretConnection(attacker, forger)
	}()
	connection, err := MakeSecretConnection(honest, ed25519.GenPrivKey())
	require.ErrorContains(t, err, "invalid remote authentication key", "a universal signature must not authenticate a peer")
	require.Nil(t, connection)
	require.NoError(t, attacker.Close())
	<-done
}
