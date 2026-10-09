package propagation

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"runtime"
	"runtime/debug"
	"testing"
	"time"

	proptypes "github.com/cometbft/cometbft/consensus/propagation/types"
	"github.com/cometbft/cometbft/crypto/merkle"
	"github.com/cometbft/cometbft/types"
)

var proposalCoreBenchSink []byte

// BenchmarkProposalCore32MB measures the proposer-side work that follows
// PrepareProposal for a 32 MB block of 64 blob transactions.
//
// types.Encode is around 90% of the result, so a change to any other step will
// sit at or below this benchmark's noise floor and needs measuring on its own.
// Not measured: PrepareProposal, transaction hashing (the fixture pre-computes
// it), the proposal signature, peer networking and the receiving side.
func BenchmarkProposalCore32MB(b *testing.B) {
	const (
		txCount = 64
		txSize  = 500_000
		// expectedParts and blockBytes pin the fixture's shape; both are
		// determined by txCount, txSize and BlockPartSizeBytes.
		expectedParts = 489
		blockBytes    = 32_000_451
	)
	txs := make(types.Txs, txCount)
	hashes := make([][]byte, txCount)
	for i := range txs {
		tx := make([]byte, txSize)
		binary.LittleEndian.PutUint64(tx, uint64(i))
		for j := 8; j < len(tx); j++ {
			tx[j] = byte((j*31 + i*17) % 251)
		}
		txs[i] = tx
		hashes[i] = txs[i].Hash()
	}

	block := types.MakeBlock(1, types.Data{Txs: txs}, &types.Commit{}, nil)
	block.ChainID = "proposal-core-benchmark"
	block.Time = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	block.SetCachedHashes(hashes)

	var original, parity *types.PartSet
	var lastLen int
	var compact proptypes.CompactBlock
	var signBytes []byte
	var err error
	var start time.Time
	var excluded time.Duration
	var originalDigest, parityDigest [sha256.Size]byte
	b.SetBytes(int64(blockBytes))
	b.ReportAllocs()
	for i := -1; i < b.N; i++ {
		if i == 0 {
			runtime.GC()
			defer debug.SetGCPercent(debug.SetGCPercent(-1))
			b.ResetTimer()
			start = time.Now()
		}
		original, err = block.MakePartSet(types.BlockPartSizeBytes)
		if err != nil {
			b.Fatal(err)
		}
		parity, lastLen, err = types.Encode(original, types.BlockPartSizeBytes)
		if err != nil {
			b.Fatal(err)
		}

		metadata := make([]proptypes.TxMetaData, len(original.TxPos))
		for j, pos := range original.TxPos {
			metadata[j] = proptypes.TxMetaData{Start: pos.Start, End: pos.End, Hash: hashes[j]}
		}
		compact = proptypes.CompactBlock{
			Proposal: types.Proposal{
				Height:  1,
				BlockID: types.BlockID{Hash: block.Hash(), PartSetHeader: original.Header()},
			},
			LastLen:     uint32(lastLen),
			BpHash:      parity.Hash(),
			Blobs:       metadata,
			PartsHashes: extractHashes(original, parity),
		}
		compact.SetProofCache(extractProofs(original, parity))
		signBytes, err = compact.SignBytes()
		if err != nil {
			b.Fatal(err)
		}

		// Checking the output costs ~64 MiB of hashing, and collecting the
		// garbage this iteration produced costs more, so both happen with the
		// clock stopped and their cost is subtracted from the manual timer.
		if i >= 0 {
			b.StopTimer()
		}
		pause := time.Now()

		// Pin the produced bytes, not just their self-consistency. The warm-up
		// records the digests and every measured iteration must reproduce
		// them, so an optimization that hands out an aliased, stale or
		// mis-encoded buffer fails here instead of being measured as a win.
		digest := sha256.Sum256(original.GetBytes())
		parityDigestNow := sha256.Sum256(parity.GetBytes())
		if i < 0 {
			originalDigest, parityDigest = digest, parityDigestNow
		} else if digest != originalDigest || parityDigestNow != parityDigest {
			b.Fatal("part set bytes changed between iterations")
		}

		// One iteration allocates over 100 MB. Collecting between iterations
		// keeps the heap bounded without putting a collection inside the
		// measured region.
		runtime.GC()

		if i >= 0 {
			excluded += time.Since(pause)
			b.StartTimer()
		}
	}
	b.StopTimer()
	elapsed := time.Since(start) - excluded

	// The fixture is exactly deterministic, so the expected shape is exact. A
	// mismatch means the fixture drifted and the numbers are not comparable
	// with earlier runs.
	if len(original.TxPos) != txCount || original.Total() != expectedParts {
		b.Fatalf("unexpected original part count or metadata: %d parts, %d txs", original.Total(), len(original.TxPos))
	}
	if int(original.ByteSize()) != blockBytes {
		b.Fatalf("fixture drifted: block is %d bytes, expected %d", original.ByteSize(), blockBytes)
	}
	if parity.Total() != original.Total() || lastLen <= 0 || len(signBytes) == 0 {
		b.Fatalf("invalid parity or compact block: %d parity parts, last len %d, sign bytes %d", parity.Total(), lastLen, len(signBytes))
	}
	if root, _ := merkle.ProofsFromLeafHashes(compact.PartsHashes[:original.Total()]); !bytes.Equal(root, original.Hash()) {
		b.Fatal("original commitment mismatch")
	}
	if root, _ := merkle.ProofsFromLeafHashes(compact.PartsHashes[original.Total():]); !bytes.Equal(root, parity.Hash()) {
		b.Fatal("parity commitment mismatch")
	}
	// The commitments above are derived from the same leaf hashes the part sets
	// were built with, so they only check tree aggregation. Dropping a part and
	// reconstructing it from the parity is what checks the content: it is the
	// only assertion here that fails if the parity bytes are wrong.
	partial := types.NewPartSetFromHeader(original.Header(), types.BlockPartSizeBytes)
	for j := 1; j < int(original.Total()); j++ {
		if _, err := partial.AddPart(original.GetPart(j)); err != nil {
			b.Fatal(err)
		}
	}
	recovered, _, err := types.Decode(partial, parity, lastLen)
	if err != nil {
		b.Fatal(err)
	}
	if !bytes.Equal(recovered.GetBytes(), original.GetBytes()) {
		b.Fatal("parity does not reconstruct the block bytes")
	}
	proposalCoreBenchSink = signBytes
	b.ReportMetric(float64(elapsed.Nanoseconds())/float64(b.N)/1_000_000, "proposal_ms/op")
}
