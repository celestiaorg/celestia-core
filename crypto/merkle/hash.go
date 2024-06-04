package merkle

import (
	"hash"

	"github.com/cometbft/cometbft/crypto/tmhash"
)

var (
	leafPrefix  = []byte{0}
	innerPrefix = []byte{1}
)

// returns tmhash(<empty>)
func emptyHash() []byte {
	return tmhash.Sum([]byte{})
}

// returns tmhash(0x00 || leaf)
func leafHash(leaf []byte) []byte {
	return tmhash.Sum(append(leafPrefix, leaf...))
}

// returns tmhash(0x00 || leaf)
func leafHashOpt(h hash.Hash, leaf []byte) []byte {
	h.Reset()
	h.Write(leafPrefix)
	h.Write(leaf)
	return h.Sum(nil)
}

// returns tmhash(0x01 || left || right)
func innerHash(left []byte, right []byte) []byte {
	return tmhash.Sum(append(innerPrefix, append(left, right...)...))
}

// returns tmhash(0x01 || left || right)
func innerHashOpt(h hash.Hash, left []byte, right []byte) []byte {
	h.Reset()
	h.Write(innerPrefix)
	h.Write(left)
	h.Write(right)
	return h.Sum(nil)
}
