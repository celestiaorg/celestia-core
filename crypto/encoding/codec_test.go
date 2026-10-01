package encoding_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/crypto"
	"github.com/cometbft/cometbft/crypto/ed25519"
	"github.com/cometbft/cometbft/crypto/encoding"
	"github.com/cometbft/cometbft/crypto/mldsa65"
	"github.com/cometbft/cometbft/crypto/secp256k1"
)

func TestPubKeyProtoRoundTrip(t *testing.T) {
	mldsaPriv, err := mldsa65.GenPrivKey()
	require.NoError(t, err)

	testCases := []struct {
		name   string
		pubKey crypto.PubKey
	}{
		{"ed25519", ed25519.GenPrivKey().PubKey()},
		{"secp256k1", secp256k1.GenPrivKey().PubKey()},
		{"mldsa65", mldsaPriv.PubKey()},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			pb, err := encoding.PubKeyToProto(tc.pubKey)
			require.NoError(t, err)

			got, err := encoding.PubKeyFromProto(pb)
			require.NoError(t, err)
			require.True(t, tc.pubKey.Equals(got))
			require.Equal(t, tc.pubKey.Type(), got.Type())
		})
	}
}
