package types

import (
	"github.com/lazyledger/lazyledger-core/crypto/ed25519"
	cryptoenc "github.com/lazyledger/lazyledger-core/crypto/encoding"
)

const (
	PubKeyEd25519 = "ed25519"
)

func Ed25519ValidatorUpdate(pk []byte, power int64) ValidatorUpdate {
	pke := ed25519.PubKey(pk)
	pkp, err := cryptoenc.PubKeyToProto(pke)
	if err != nil {
		panic(err)
	}

	return ValidatorUpdate{
		// Address:
		PubKey: pkp,
		Power:  power,
	}
}
