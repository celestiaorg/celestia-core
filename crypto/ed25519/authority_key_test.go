package ed25519

import (
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateAuthorityKey(t *testing.T) {
	for range 64 {
		require.NoError(t, GenPrivKey().PubKey().(PubKey).ValidateAuthorityKey())
	}
	for _, encoded := range []string{
		"0100000000000000000000000000000000000000000000000000000000000000", // identity
		"ecffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f", // order 2
		"26e8958fc2b227b045c3f489f2ef98f0d5dfac05d3c63339b13802886d53fc05", // order 8
		"edffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f", // noncanonical
		"0100000000000000000000000000000000000000000000000000000000000080", // x=0, sign=1
		"0b485cfc0eecc619440448436f8fc9df40566f2369e72400281454cb552afb52", // off curve
		"d03785ba6fb4990c5b4c743b53fc0ba2eb8b6ee2886e6c776eac2e1e7cb42cc4", // mixed order
	} {
		key, err := hex.DecodeString(encoded)
		require.NoError(t, err)
		require.Error(t, PubKey(key).ValidateAuthorityKey(), encoded)
	}
	for _, size := range []int{0, 31, 33} {
		require.Error(t, PubKey(make([]byte, size)).ValidateAuthorityKey())
	}
}

func TestAuthorityAdmissionDoesNotChangeZIP215(t *testing.T) {
	key := make(PubKey, PubKeySize)
	key[0] = 1
	sig := make([]byte, SignatureSize)
	sig[0] = 1
	require.Error(t, key.ValidateAuthorityKey())
	for _, message := range [][]byte{[]byte("vote"), []byte("another vote")} {
		require.True(t, key.VerifySignature(message, sig))
		batch := NewBatchVerifier()
		require.NoError(t, batch.Add(key, message, sig))
		ok, valid := batch.Verify()
		require.True(t, ok)
		require.Equal(t, []bool{true}, valid)
	}
}

func BenchmarkValidateAuthorityKey(b *testing.B) {
	key := GenPrivKey().PubKey().(PubKey)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if err := key.ValidateAuthorityKey(); err != nil {
			b.Fatal(err)
		}
	}
}
