package chunked

import (
	"crypto/sha256"
	"hash"
	"math/bits"

	"github.com/cometbft/cometbft/crypto/merkle"
)

// RFC 6962 prefixes — must match the merkle package's leafHash/innerHash so
// proofs we generate verify against parts_root computed by merkle.ProofsFromByteSlices.
const (
	rfc6962LeafPrefix  = 0x00
	rfc6962InnerPrefix = 0x01
)

func innerHashLeafProofs(sha hash.Hash, left, right []byte) []byte {
	sha.Reset()
	sha.Write([]byte{rfc6962InnerPrefix})
	sha.Write(left)
	sha.Write(right)
	return sha.Sum(nil)
}

// getSplitPoint returns the largest power of 2 less than length. Matches
// the merkle package's split rule so the tree shape is identical.
func getSplitPoint(length int64) int64 {
	if length < 1 {
		return 0
	}
	if length == 1 {
		return 0
	}
	uLength := uint(length)
	bitlen := bits.Len(uLength)
	k := int64(1 << uint(bitlen-1))
	if k == length {
		k >>= 1
	}
	return k
}

// proofsFromLeafHashes computes Merkle inclusion proofs given a list of
// already-computed leaf hashes (each = sha256(0x00||data)). Returns the
// root and one Proof per leaf, matching the RFC 6962 tree shape used by
// github.com/cometbft/cometbft/crypto/merkle.
//
// We need this because SeenLargeTx carries all leaf hashes but the receiver
// may not yet hold the corresponding chunk data; with leaf hashes + tree
// shape we can still compute proofs to serve chunks we DO have to peers,
// without waiting for full reconstruction.
func proofsFromLeafHashes(leaves [][]byte) ([]byte, []*merkle.Proof) {
	n := len(leaves)
	if n == 0 {
		return sha256.New().Sum(nil), nil
	}
	proofs := make([]*merkle.Proof, n)
	for i := range proofs {
		proofs[i] = &merkle.Proof{
			Total:    int64(n),
			Index:    int64(i),
			LeafHash: leaves[i],
		}
	}
	if n == 1 {
		return leaves[0], proofs
	}
	sha := sha256.New()
	var build func(start, end int) []byte
	build = func(start, end int) []byte {
		if end-start == 1 {
			return leaves[start]
		}
		k := getSplitPoint(int64(end - start))
		leftHash := build(start, start+int(k))
		rightHash := build(start+int(k), end)
		// Sibling on the path: right hash is an aunt for every leaf in the
		// left subtree; left hash is an aunt for every leaf in the right.
		for i := start; i < start+int(k); i++ {
			proofs[i].Aunts = append(proofs[i].Aunts, rightHash)
		}
		for i := start + int(k); i < end; i++ {
			proofs[i].Aunts = append(proofs[i].Aunts, leftHash)
		}
		return innerHashLeafProofs(sha, leftHash, rightHash)
	}
	root := build(0, n)
	return root, proofs
}
