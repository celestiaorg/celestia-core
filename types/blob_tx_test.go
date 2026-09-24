package types

import (
	"crypto/sha256"
	"testing"

	squaretx "github.com/celestiaorg/go-square/v3/tx"
	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/internal/test/blobtx"
)

func TestExtractBlobTxCompatibility(t *testing.T) {
	for _, tc := range blobtx.Cases(t) {
		t.Run(tc.Name, func(t *testing.T) {
			inner, ok := ExtractBlobTx(tc.Wire)
			require.Equal(t, tc.Recognized, ok)
			require.Equal(t, tc.Inner, []byte(inner))
			legacy, legacyOK := UnmarshalBlobTx(tc.Wire)
			require.Equal(t, legacyOK, ok)
			if ok {
				require.Equal(t, legacy.Tx, []byte(inner))
			}
			decoded, _, err := squaretx.UnmarshalBlobTx(tc.Wire)
			if tc.Valid {
				require.NoError(t, err)
				require.NotNil(t, decoded)
			} else {
				require.Error(t, err)
			}
			hash := sha256.Sum256(tc.Inner)
			require.Equal(t, hash[:], Tx(tc.Wire).Hash())
			require.Equal(t, TxKey(hash), Tx(tc.Wire).Key())
		})
	}
}

func FuzzExtractBlobTxCompatibility(f *testing.F) {
	for _, tc := range blobtx.Cases(f) {
		f.Add(tc.Wire)
	}
	f.Fuzz(func(t *testing.T, wire []byte) {
		legacy, recognized := UnmarshalBlobTx(wire)
		inner, ok := ExtractBlobTx(wire)
		require.Equal(t, recognized, ok)
		expected := wire
		if recognized {
			expected = legacy.Tx
		}
		require.Equal(t, expected, []byte(inner))
		// Hash and Key intentionally have different IndexWrapper precedence.
		keyInput := expected
		if !recognized {
			if index, ok := UnmarshalIndexWrapper(wire); ok {
				keyInput = index.Tx
			}
		}
		hashInput := expected
		if index, ok := UnmarshalIndexWrapper(wire); ok {
			hashInput = index.Tx
		}
		hash := sha256.Sum256(hashInput)
		require.Equal(t, hash[:], Tx(wire).Hash())
		require.Equal(t, TxKey(sha256.Sum256(keyInput)), Tx(wire).Key())
	})
}
