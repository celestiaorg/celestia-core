package cat

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	protomem "github.com/cometbft/cometbft/proto/tendermint/mempool"
	"github.com/cometbft/cometbft/types"
)

// Large-tx fast-path tuning. These are hardcoded defaults rather than operator
// TOML knobs: they are protocol-level values that should "always use these
// values" for a given network. Celestia overrides them at startup through
// ReactorOptions, the same way it overrides MaxTxSize and MaxGossipDelay.
const (
	// DefaultLargeTxThreshold is the tx size (in bytes) at or above which the
	// chunked fast path is used instead of whole-object SeenTx/WantTx/Txs
	// transfer.
	DefaultLargeTxThreshold = 1 * 1024 * 1024 // 1 MiB

	// DefaultLargeTxChunkSize is the size (in bytes) of each chunk a large tx is
	// split into.
	DefaultLargeTxChunkSize = 256 * 1024 // 256 KiB

	// DefaultLargeTxRequestParallelism is the number of peers a receiver fetches
	// chunks from in parallel for a single large tx.
	DefaultLargeTxRequestParallelism = 4

	// DefaultLargeTxMaxInflightChunksPerPeer bounds the number of outstanding
	// chunk requests to a single peer for a single large tx.
	DefaultLargeTxMaxInflightChunksPerPeer = 8

	// DefaultLargeTxChunkTimeout is how long a receiver waits for a chunk before
	// re-requesting it from an alternate peer.
	DefaultLargeTxChunkTimeout = 75 * time.Millisecond

	// DefaultLargeTxReconstructionTimeout bounds the total time a reconstruction
	// session may stay open before being abandoned.
	DefaultLargeTxReconstructionTimeout = 500 * time.Millisecond

	// DefaultLargeTxMaxAdvertisePeers caps how many peers a TxManifest is
	// advertised to.
	DefaultLargeTxMaxAdvertisePeers = 15

	// DefaultLargeTxOptimisticPushChunks is the number of leading chunks the
	// sender optimistically pushes to top-scored peers, removing one request
	// round trip for early reconstruction.
	DefaultLargeTxOptimisticPushChunks = 2

	// DefaultLargeTxPeerScoreHalflife is the decay halflife for per-peer chunk
	// performance scores.
	DefaultLargeTxPeerScoreHalflife = 30 * time.Second

	// chunkHashSize is the length in bytes of each per-chunk hash.
	chunkHashSize = sha256.Size

	// minLargeTxChunkSize bounds chunk_size from below so a malicious manifest
	// cannot declare a tiny chunk size and force a huge chunk_count / hash list.
	minLargeTxChunkSize = 16 * 1024 // 16 KiB

	// maxLargeTxChunkSize bounds chunk_size from above so a single chunk fits
	// comfortably below the chunk channel's RecvMessageCapacity.
	maxLargeTxChunkSize = 4 * 1024 * 1024 // 4 MiB

	// maxChunkCount bounds the number of chunks (and therefore chunk hashes) in
	// a single manifest, preventing unbounded memory/CPU from a malicious
	// manifest regardless of the negotiated chunk size.
	maxChunkCount = 8192
)

var (
	errManifestBadTxKey      = errors.New("manifest tx_key has incorrect length")
	errManifestBadTxSize     = errors.New("manifest tx_size is zero or exceeds max tx bytes")
	errManifestBadChunkSize  = errors.New("manifest chunk_size out of bounds")
	errManifestBadChunkCount = errors.New("manifest chunk_count does not match tx_size and chunk_size")
	errManifestTooManyChunks = errors.New("manifest chunk_count exceeds maximum")
	errManifestBadHashCount  = errors.New("manifest chunk_hashes count does not match chunk_count")
	errManifestBadHashLen    = errors.New("manifest chunk hash has incorrect length")
	errManifestSignerTooLong = errors.New("manifest signer field exceeds maximum length")

	errChunkIndexOutOfRange = errors.New("chunk index out of range")
	errChunkWrongSize       = errors.New("chunk data has unexpected size")
	errChunkHashMismatch    = errors.New("chunk data does not match manifest hash")

	errReconstructIncomplete = errors.New("cannot reconstruct: missing chunks")
	errReconstructBadSize    = errors.New("reconstructed tx has unexpected size")
	errReconstructBadTxKey   = errors.New("reconstructed tx does not match manifest tx_key")
)

// numChunks returns the number of chunkSize-byte chunks required to hold a tx of
// txSize bytes (the final chunk may be smaller).
func numChunks(txSize, chunkSize int) int {
	if chunkSize <= 0 {
		return 0
	}
	return (txSize + chunkSize - 1) / chunkSize
}

// chunkHash returns the hash used to identify and verify a chunk's contents.
func chunkHash(data []byte) []byte {
	sum := sha256.Sum256(data)
	return sum[:]
}

// chunkBounds returns the [start, end) byte offsets of chunk index within a tx
// of length txSize split into chunkSize chunks.
func chunkBounds(txSize, chunkSize, index int) (int, int) {
	start := index * chunkSize
	end := start + chunkSize
	if end > txSize {
		end = txSize
	}
	return start, end
}

// buildManifest splits tx into chunkSize chunks, hashes each, and returns a
// TxManifest describing it. key must be the mempool key of tx (types.Tx.Key()).
func buildManifest(key types.TxKey, tx []byte, chunkSize int, signer []byte, sequence, priority uint64) *protomem.TxManifest {
	count := numChunks(len(tx), chunkSize)
	hashes := make([][]byte, count)
	for i := 0; i < count; i++ {
		start, end := chunkBounds(len(tx), chunkSize, i)
		hashes[i] = chunkHash(tx[start:end])
	}
	return &protomem.TxManifest{
		TxKey:       key[:],
		TxSize:      uint32(len(tx)),
		ChunkSize:   uint32(chunkSize),
		ChunkCount:  uint32(count),
		ChunkHashes: hashes,
		Sequence:    sequence,
		Signer:      signer,
		Priority:    priority,
	}
}

// chunkData returns the bytes of chunk index of a (locally held) tx described by
// manifest m. It assumes the caller still holds the full tx bytes.
func chunkData(m *protomem.TxManifest, tx []byte, index int) ([]byte, error) {
	if index < 0 || index >= int(m.ChunkCount) {
		return nil, errChunkIndexOutOfRange
	}
	start, end := chunkBounds(len(tx), int(m.ChunkSize), index)
	if start > len(tx) || end > len(tx) {
		return nil, errChunkIndexOutOfRange
	}
	return tx[start:end], nil
}

// validateTxManifest checks a manifest received from a peer for internal
// consistency and resource-safety before any reconstruction state is allocated.
func validateTxManifest(m *protomem.TxManifest, maxTxBytes int) error {
	if len(m.TxKey) != types.TxKeySize {
		return errManifestBadTxKey
	}
	if m.TxSize == 0 || int64(m.TxSize) > int64(maxTxBytes) {
		return errManifestBadTxSize
	}
	if m.ChunkSize < minLargeTxChunkSize || m.ChunkSize > maxLargeTxChunkSize {
		return errManifestBadChunkSize
	}
	expected := numChunks(int(m.TxSize), int(m.ChunkSize))
	if int(m.ChunkCount) != expected {
		return errManifestBadChunkCount
	}
	if m.ChunkCount == 0 || m.ChunkCount > maxChunkCount {
		return errManifestTooManyChunks
	}
	if len(m.ChunkHashes) != int(m.ChunkCount) {
		return errManifestBadHashCount
	}
	for _, h := range m.ChunkHashes {
		if len(h) != chunkHashSize {
			return errManifestBadHashLen
		}
	}
	if len(m.Signer) > maxSignerLength {
		return errManifestSignerTooLong
	}
	return nil
}

// expectedChunkSize returns the expected byte length of chunk index given the
// manifest's tx_size and chunk_size.
func expectedChunkSize(m *protomem.TxManifest, index int) int {
	start, end := chunkBounds(int(m.TxSize), int(m.ChunkSize), index)
	return end - start
}

// verifyChunk checks that data is the correct length for chunk index and hashes
// to the value advertised in the manifest.
func verifyChunk(m *protomem.TxManifest, index int, data []byte) error {
	if index < 0 || index >= int(m.ChunkCount) {
		return errChunkIndexOutOfRange
	}
	if len(data) != expectedChunkSize(m, index) {
		return errChunkWrongSize
	}
	if !bytes.Equal(chunkHash(data), m.ChunkHashes[index]) {
		return errChunkHashMismatch
	}
	return nil
}

// reconstructTx concatenates chunks (in index order) into the full tx bytes and
// verifies the result against the manifest's tx_size and tx_key. chunks must be
// indexed 0..chunk_count-1; a nil entry means that chunk is still missing.
func reconstructTx(m *protomem.TxManifest, chunks [][]byte) ([]byte, error) {
	if len(chunks) != int(m.ChunkCount) {
		return nil, errReconstructIncomplete
	}
	tx := make([]byte, 0, int(m.TxSize))
	for _, c := range chunks {
		if c == nil {
			return nil, errReconstructIncomplete
		}
		tx = append(tx, c...)
	}
	if len(tx) != int(m.TxSize) {
		return nil, errReconstructBadSize
	}
	key := types.Tx(tx).Key()
	if !bytes.Equal(key[:], m.TxKey) {
		return nil, fmt.Errorf("%w: got %x want %x", errReconstructBadTxKey, key[:], m.TxKey)
	}
	return tx, nil
}
