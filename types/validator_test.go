package types

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/crypto/ed25519"
	"github.com/cometbft/cometbft/crypto/secp256k1"
	"github.com/cometbft/cometbft/crypto/sr25519"
)

func TestValidatorProtoBuf(t *testing.T) {
	val, _ := RandValidator(true, 100)
	testCases := []struct {
		msg      string
		v1       *Validator
		expPass1 bool
		expPass2 bool
	}{
		{"success validator", val, true, true},
		{"failure empty", &Validator{}, false, false},
		{"failure nil", nil, false, false},
	}
	for _, tc := range testCases {
		protoVal, err := tc.v1.ToProto()

		if tc.expPass1 {
			require.NoError(t, err, tc.msg)
		} else {
			require.Error(t, err, tc.msg)
		}

		val, err := ValidatorFromProto(protoVal)
		if tc.expPass2 {
			require.NoError(t, err, tc.msg)
			require.Equal(t, tc.v1, val, tc.msg)
		} else {
			require.Error(t, err, tc.msg)
		}
	}
}

func TestValidatorValidateBasic(t *testing.T) {
	priv := NewMockPV()
	pubKey, _ := priv.GetPubKey()
	testCases := []struct {
		val *Validator
		err bool
		msg string
	}{
		{
			val: NewValidator(pubKey, 1),
			err: false,
			msg: "",
		},
		{
			val: nil,
			err: true,
			msg: "nil validator",
		},
		{
			val: &Validator{
				PubKey: nil,
			},
			err: true,
			msg: "validator does not have a public key",
		},
		{
			val: NewValidator(pubKey, -1),
			err: true,
			msg: "validator has negative voting power",
		},
		{
			val: &Validator{
				PubKey:  pubKey,
				Address: nil,
			},
			err: true,
			msg: fmt.Sprintf("validator address is incorrectly derived from pubkey. Exp: %v, got ", pubKey.Address()),
		},
		{
			val: &Validator{
				PubKey:  pubKey,
				Address: []byte{'a'},
			},
			err: true,
			msg: fmt.Sprintf("validator address is incorrectly derived from pubkey. Exp: %v, got 61", pubKey.Address()),
		},
		{
			val: &Validator{PubKey: ed25519.PubKey{}},
			err: true,
			msg: "validator ed25519 public key has size 0, want 32",
		},
		{
			val: &Validator{PubKey: secp256k1.PubKey(make([]byte, 5))},
			err: true,
			msg: "validator secp256k1 public key has size 5, want 33",
		},
		{
			val: &Validator{PubKey: sr25519.PubKey(make([]byte, 31))},
			err: true,
			msg: "validator sr25519 public key has size 31, want 32",
		},
	}

	for _, tc := range testCases {
		err := tc.val.ValidateBasic()
		if tc.err {
			if assert.Error(t, err) {
				assert.Equal(t, tc.msg, err.Error())
			}
		} else {
			assert.NoError(t, err)
		}
	}
}
