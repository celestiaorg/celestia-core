package merkle

import (
	"hash"

	"github.com/cometbft/cometbft/crypto/tmhash"
)

// TODO: make these have a large predefined capacity
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
func leafHashOpt(s hash.Hash, leaf []byte) []byte {
	s.Reset()
	s.Write(leafPrefix)
	s.Write(leaf)
	return s.Sum(nil)
}

func LeafHash(leaf []byte) []byte {
	return leafHash(leaf)
}

// returns tmhash(0x01 || left || right)
func innerHash(left []byte, right []byte) []byte {
	return tmhash.SumMany(innerPrefix, left, right)
}

func innerHashOpt(s hash.Hash, left []byte, right []byte) []byte {
	s.Reset()
	s.Write(innerPrefix)
	s.Write(left)
	s.Write(right)
	return s.Sum(nil)
}
