package crypto

import (
	"crypto/sha256"
)

func Sha256(bytes []byte) []byte {
	hasher := sha256.New()
	hasher.Write(bytes) // nolint
	return hasher.Sum(nil)
}
