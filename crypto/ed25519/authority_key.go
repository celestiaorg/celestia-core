package ed25519

import (
	"errors"

	"github.com/oasisprotocol/curve25519-voi/curve"
)

// ValidateAuthorityKey requires a canonical encoding of a non-identity point in
// the prime-order subgroup. Use this when granting authority to a new key, not
// when verifying signatures from historical validator sets: VerifySignature and
// BatchVerifier intentionally retain their ZIP-215 acceptance rules.
func (pubKey PubKey) ValidateAuthorityKey() error {
	if len(pubKey) != PubKeySize {
		return errors.New("ed25519 authority key has incorrect length")
	}
	compressed, err := curve.NewCompressedEdwardsYFromBytes(pubKey)
	if err != nil {
		return err
	}
	if !compressed.IsCanonicalVartime() {
		return errors.New("ed25519 authority key is not canonical")
	}
	point, err := curve.NewEdwardsPoint().SetCompressedY(compressed)
	if err != nil {
		return errors.New("ed25519 authority key is not on the curve")
	}
	if point.IsSmallOrder() {
		return errors.New("ed25519 authority key has small order")
	}
	if !point.IsTorsionFree() {
		return errors.New("ed25519 authority key is not in the prime-order subgroup")
	}
	return nil
}
