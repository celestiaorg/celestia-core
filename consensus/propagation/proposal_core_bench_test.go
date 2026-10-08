package propagation

import (
	"bytes"
	"encoding/binary"
	"testing"
	"time"

	proptypes "github.com/cometbft/cometbft/consensus/propagation/types"
	"github.com/cometbft/cometbft/crypto/merkle"
	"github.com/cometbft/cometbft/types"
)

var proposalCoreBenchSink []byte

// BenchmarkProposalCore32MB measures proposer-side work after PrepareProposal
// returns 64 blob transactions of 500,000 bytes each. The fixture is built
// once, outside the measured interval.
func BenchmarkProposalCore32MB(b *testing.B) {
	b.StopTimer()
	const txCount = 64
	const txSize = 500_000
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
	block.Header.ChainID = "proposal-core-benchmark"
	block.Header.Time = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	block.SetCachedHashes(hashes)

	var original, parity *types.PartSet
	var lastLen int
	var compact proptypes.CompactBlock
	var signBytes []byte
	var err error
	var start time.Time
	for i := -1; i < b.N; i++ {
		if i == 0 {
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
	}
	elapsed := time.Since(start)
	b.StopTimer()

	if len(original.TxPos) != txCount || original.Total() < 488 || original.Total() > 490 {
		b.Fatalf("unexpected original part count or metadata: %d parts, %d txs", original.Total(), len(original.TxPos))
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
	proposalCoreBenchSink = signBytes
	b.ReportMetric(float64(elapsed.Nanoseconds())/float64(b.N)/1_000_000, "proposal_ms/op")
}
