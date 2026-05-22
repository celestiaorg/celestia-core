// Package chunked implements the chunked + Reed-Solomon erasure-coded
// transaction propagation primitives described in ADR-012.
package chunked

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"

	"github.com/cometbft/cometbft/crypto/merkle"
	"github.com/cometbft/cometbft/types"
	"github.com/klauspost/reedsolomon"
)

// ChunkSize is the on-wire chunk size. Matches consensus block parts so the
// same Reed-Solomon and Merkle primitives can be shared.
const ChunkSize = int(types.BlockPartSizeBytes)

var (
	ErrEmptyBody          = errors.New("chunked: empty tx body")
	ErrInsufficientChunks = errors.New("chunked: not enough chunks to decode")
	ErrChunkSize          = errors.New("chunked: chunk has wrong size")
	ErrChunkCount         = errors.New("chunked: chunk count must be 1 or even (2K)")
	ErrLastLength         = errors.New("chunked: last_length inconsistent with body")
	ErrSingleChunkProof   = errors.New("chunked: single-chunk hash mismatch")
)

// EncodedTx is the result of encoding a transaction body for chunked gossip.
//
// For multi-chunk txs (K >= 2):
//   - Originals contains K padded chunks (last original zero-padded to ChunkSize).
//   - Parity contains K Reed-Solomon parity chunks of ChunkSize bytes each.
//   - LeafHashes and PartsRoot commit to the 2K padded chunks in order.
//
// For single-chunk txs (K == 1) parity is skipped entirely:
//   - Originals is a single chunk of LastLength bytes (no padding).
//   - PartsRoot == sha256(chunk); no Merkle proofs are needed.
type EncodedTx struct {
	Body       []byte
	Originals  [][]byte
	Parity     [][]byte
	LeafHashes [][]byte
	PartsRoot  []byte
	Proofs     []*merkle.Proof
	LastLength uint32
}

// NumParts returns the total number of chunks on the wire (2K, or 1).
func (e *EncodedTx) NumParts() uint32 { return uint32(len(e.Originals) + len(e.Parity)) }

// NumOriginals returns K, the original-chunk count.
func (e *EncodedTx) NumOriginals() uint32 { return uint32(len(e.Originals)) }

// Chunk returns the bytes for chunk at index i, where 0..K-1 are originals
// and K..2K-1 are parity. For K == 1 only index 0 is valid.
func (e *EncodedTx) Chunk(i uint32) []byte {
	k := uint32(len(e.Originals))
	if i < k {
		return e.Originals[i]
	}
	return e.Parity[i-k]
}

// Encode splits body into 64 KiB chunks, Reed-Solomon (K,K) extends them with
// K parity chunks, and computes the Merkle root + per-chunk inclusion proofs
// over the 2K padded chunks.
//
// For bodies that fit in a single chunk, Encode skips parity: the result has
// one Originals entry of len(body) bytes, no Parity, and PartsRoot is the
// SHA-256 of that chunk.
func Encode(body []byte) (*EncodedTx, error) {
	if len(body) == 0 {
		return nil, ErrEmptyBody
	}
	k := (len(body) + ChunkSize - 1) / ChunkSize
	lastLen := uint32(len(body) - (k-1)*ChunkSize)
	if k == 1 {
		return encodeSingle(body, lastLen), nil
	}
	return encodeMulti(body, k, lastLen)
}

func encodeSingle(body []byte, lastLen uint32) *EncodedTx {
	chunk := make([]byte, len(body))
	copy(chunk, body)
	h := sha256.Sum256(chunk)
	return &EncodedTx{
		Body:       body,
		Originals:  [][]byte{chunk},
		LeafHashes: [][]byte{append([]byte(nil), h[:]...)},
		PartsRoot:  append([]byte(nil), h[:]...),
		LastLength: lastLen,
	}
}

func encodeMulti(body []byte, k int, lastLen uint32) (*EncodedTx, error) {
	chunks := make([][]byte, 2*k)
	for i := 0; i < k-1; i++ {
		chunk := make([]byte, ChunkSize)
		copy(chunk, body[i*ChunkSize:(i+1)*ChunkSize])
		chunks[i] = chunk
	}
	// Last original: pad to ChunkSize before RS encode and before hashing.
	last := make([]byte, ChunkSize)
	copy(last, body[(k-1)*ChunkSize:])
	chunks[k-1] = last
	// Parity chunks share a backing buffer for cache-friendly RS encode.
	parityBuf := make([]byte, k*ChunkSize)
	for i := 0; i < k; i++ {
		chunks[k+i] = parityBuf[i*ChunkSize : (i+1)*ChunkSize]
	}

	enc, err := reedsolomon.New(k, k)
	if err != nil {
		return nil, fmt.Errorf("chunked: new reed-solomon: %w", err)
	}
	if err := enc.Encode(chunks); err != nil {
		return nil, fmt.Errorf("chunked: encode: %w", err)
	}

	root, proofs := merkle.ParallelProofsFromByteSlices(chunks)
	leaves := make([][]byte, len(chunks))
	for i, p := range proofs {
		leaves[i] = p.LeafHash
	}

	return &EncodedTx{
		Body:       body,
		Originals:  chunks[:k],
		Parity:     chunks[k:],
		LeafHashes: leaves,
		PartsRoot:  root,
		Proofs:     proofs,
		LastLength: lastLen,
	}, nil
}

// Decode reconstructs the original body from a chunk slice of length numParts.
// Missing chunks must be nil. At least K = numParts/2 non-nil chunks are
// required (or the single chunk for numParts == 1).
//
// Decode does NOT verify chunks against a parts_root; the caller is expected
// to have done that on receipt with VerifyChunk. It does check the final
// reconstructed body length against lastLength.
func Decode(chunks [][]byte, lastLength uint32) ([]byte, error) {
	n := len(chunks)
	switch {
	case n == 0:
		return nil, ErrInsufficientChunks
	case n == 1:
		if chunks[0] == nil {
			return nil, ErrInsufficientChunks
		}
		if uint32(len(chunks[0])) != lastLength {
			return nil, ErrLastLength
		}
		out := make([]byte, len(chunks[0]))
		copy(out, chunks[0])
		return out, nil
	case n%2 != 0:
		return nil, ErrChunkCount
	}
	k := n / 2
	if lastLength == 0 || lastLength > uint32(ChunkSize) {
		return nil, ErrLastLength
	}

	work := make([][]byte, n)
	have := 0
	for i, c := range chunks {
		if c == nil {
			continue
		}
		if len(c) != ChunkSize {
			return nil, fmt.Errorf("%w: index %d len %d", ErrChunkSize, i, len(c))
		}
		work[i] = c
		have++
	}
	if have < k {
		return nil, ErrInsufficientChunks
	}

	enc, err := reedsolomon.New(k, k)
	if err != nil {
		return nil, fmt.Errorf("chunked: new reed-solomon: %w", err)
	}
	if err := enc.Reconstruct(work); err != nil {
		return nil, fmt.Errorf("chunked: reconstruct: %w", err)
	}

	out := make([]byte, (k-1)*ChunkSize+int(lastLength))
	for i := 0; i < k-1; i++ {
		copy(out[i*ChunkSize:], work[i])
	}
	copy(out[(k-1)*ChunkSize:], work[k-1][:lastLength])
	return out, nil
}

// VerifyChunk verifies a received chunk against the announced parts_root.
// For numParts == 1 it checks sha256(data) == partsRoot and ignores the proof.
// For numParts >= 2 it verifies the Merkle proof at the given index.
func VerifyChunk(partsRoot []byte, numParts uint32, index uint32, data []byte, proof *merkle.Proof) error {
	if numParts == 0 || index >= numParts {
		return fmt.Errorf("chunked: index %d out of range for num_parts %d", index, numParts)
	}
	if numParts == 1 {
		h := sha256.Sum256(data)
		if !bytes.Equal(h[:], partsRoot) {
			return ErrSingleChunkProof
		}
		return nil
	}
	if len(data) != ChunkSize {
		return fmt.Errorf("%w: got %d want %d", ErrChunkSize, len(data), ChunkSize)
	}
	if proof == nil {
		return errors.New("chunked: missing merkle proof")
	}
	if proof.Index != int64(index) {
		return fmt.Errorf("chunked: proof index %d mismatches advertised %d", proof.Index, index)
	}
	if proof.Total != int64(numParts) {
		return fmt.Errorf("chunked: proof total %d mismatches advertised %d", proof.Total, numParts)
	}
	return proof.Verify(partsRoot, data)
}
