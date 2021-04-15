package crypto

import (
	crand "crypto/rand"
	"encoding/hex"
	"io"
)

// This only uses the OS's randomness
func randBytes(numBytes int) []byte {
	b := make([]byte, numBytes)
	_, err := crand.Read(b)
	if err != nil {
		panic(err)
	}
	return b
}

// CRandBytes returns requested number of bytes from the OS's randomness.
func CRandBytes(numBytes int) []byte {
	return randBytes(numBytes)
}

// CRandHex returns a hex encoded string that's floor(numDigits/2) * 2 long.
//
// Note: CRandHex(24) gives 96 bits of randomness that
// are usually strong enough for most purposes.
func CRandHex(numDigits int) string {
	return hex.EncodeToString(CRandBytes(numDigits / 2))
}

// CRandSeed returns a seed from the OS's randomness.
func CRandSeed(length int) int64 {
	b := randBytes(length)
	var seed uint64
	for i := 0; i < 8; i++ {
		seed |= uint64(b[i])
		seed <<= 8
	}
	return int64(seed)
}

// CReader returns a crand.Reader.
func CReader() io.Reader {
	return crand.Reader
}
