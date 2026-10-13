package consensus

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/cometbft/cometbft/libs/autofile"
	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/p2p"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	cmttypes "github.com/cometbft/cometbft/types"
)

const (
	fullWALBlobCount      = 32
	fullWALBlobSize       = 1 << 20
	fullWALValidatorCount = 100
	fullWALSamples        = 15
)

type fullWALWorkload struct {
	proposal   msgInfo
	parts      []msgInfo
	prevotes   []msgInfo
	precommits []msgInfo
	localVote  msgInfo
	localPC    msgInfo
}

// TestFullWAL32MiB100Validators measures only the production WAL durability
// boundary. Fixture construction, WAL opening, and cleanup are outside the
// timed interval. The explicit -test.run guard keeps this campaign benchmark
// out of ordinary `go test` and `make test` runs.
func TestFullWAL32MiB100Validators(t *testing.T) {
	if testingRunPattern() != "^TestFullWAL32MiB100Validators$" {
		t.Skip("campaign benchmark runs only when selected explicitly")
	}

	workload := makeFullWALWorkload(t)
	runFullWALSample(t, workload)

	samples := make([]time.Duration, fullWALSamples)
	for i := range samples {
		samples[i] = runFullWALSample(t, workload)
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	median := samples[len(samples)/2]

	fmt.Printf("WAL_MEDIAN_MS: %.3f\n", float64(median)/float64(time.Millisecond))
	fmt.Printf("WAL_PARTS: %d\n", len(workload.parts))
}

func testingRunPattern() string {
	run := flag.Lookup("test.run")
	if run == nil {
		return ""
	}
	return run.Value.String()
}

func makeFullWALWorkload(t *testing.T) fullWALWorkload {
	t.Helper()

	txs := make([]cmttypes.Tx, 0, fullWALBlobCount)
	for i := 0; i < fullWALBlobCount; i++ {
		blob := &cmtproto.Blob{
			NamespaceId:      bytes.Repeat([]byte{byte(i + 1)}, 28),
			Data:             bytes.Repeat([]byte{byte(i + 1)}, fullWALBlobSize),
			ShareVersion:     0,
			NamespaceVersion: 0,
		}
		tx, err := cmttypes.MarshalBlobTx([]byte("deterministic-msg-pay-for-blobs"), blob)
		if err != nil {
			t.Fatal(err)
		}
		txs = append(txs, tx)
	}

	block := cmttypes.MakeBlock(1, cmttypes.MakeData(txs), &cmttypes.Commit{}, nil)
	partSet, err := block.MakePartSet(cmttypes.BlockPartSizeBytes)
	if err != nil {
		t.Fatal(err)
	}
	blockID := cmttypes.BlockID{Hash: block.Hash(), PartSetHeader: partSet.Header()}

	proposal := &cmttypes.Proposal{
		Type:      cmtproto.ProposalType,
		Height:    1,
		Round:     0,
		POLRound:  -1,
		BlockID:   blockID,
		Timestamp: time.Unix(1, 0).UTC(),
		Signature: bytes.Repeat([]byte{1}, 64),
	}

	parts := make([]msgInfo, partSet.Total())
	for i := range parts {
		parts[i] = msgInfo{Msg: &BlockPartMessage{
			Height: 1,
			Round:  0,
			Part:   partSet.GetPart(i),
		}}
	}

	prevotes := make([]msgInfo, 0, fullWALValidatorCount-1)
	precommits := make([]msgInfo, 0, fullWALValidatorCount-1)
	for i := 1; i < fullWALValidatorCount; i++ {
		prevotes = append(prevotes, fullWALBenchmarkVote(i, cmtproto.PrevoteType, blockID))
		precommits = append(precommits, fullWALBenchmarkVote(i, cmtproto.PrecommitType, blockID))
	}

	return fullWALWorkload{
		proposal:   msgInfo{Msg: &ProposalMessage{Proposal: proposal}},
		parts:      parts,
		prevotes:   prevotes,
		precommits: precommits,
		localVote:  fullWALBenchmarkVote(0, cmtproto.PrevoteType, blockID),
		localPC:    fullWALBenchmarkVote(0, cmtproto.PrecommitType, blockID),
	}
}

func fullWALBenchmarkVote(index int, typ cmtproto.SignedMsgType, blockID cmttypes.BlockID) msgInfo {
	return msgInfo{
		Msg: &VoteMessage{Vote: &cmttypes.Vote{
			Type:             typ,
			Height:           1,
			Round:            0,
			BlockID:          blockID,
			Timestamp:        time.Unix(1, int64(index)).UTC(),
			ValidatorAddress: bytes.Repeat([]byte{byte(index + 1)}, 20),
			ValidatorIndex:   int32(index),
			Signature:        bytes.Repeat([]byte{byte(index + 1)}, 64),
		}},
		PeerID: p2p.ID(fmt.Sprintf("validator-%03d", index)),
	}
}

func runFullWALSample(t *testing.T, workload fullWALWorkload) time.Duration {
	t.Helper()

	dir, err := os.MkdirTemp("", "full-wal-benchmark-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	wal, err := NewWAL(
		filepath.Join(dir, "wal"),
		autofile.GroupHeadSizeLimit(1<<30),
		autofile.GroupCheckDuration(time.Hour),
	)
	if err != nil {
		t.Fatal(err)
	}
	wal.SetFlushInterval(time.Hour)
	wal.SetLogger(log.NewNopLogger())
	if err := wal.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := wal.Stop(); err != nil {
			t.Error(err)
		}
		wal.Wait()
	}()

	state := &State{wal: wal}
	start := time.Now()
	state.writeInternalMsgToWAL(workload.proposal)
	for _, part := range workload.parts {
		state.writeInternalMsgToWAL(part)
	}
	state.writeInternalMsgToWAL(workload.localVote)
	for _, vote := range workload.prevotes {
		if err := wal.Write(vote); err != nil {
			t.Fatal(err)
		}
	}
	state.writeInternalMsgToWAL(workload.localPC)
	for _, vote := range workload.precommits {
		if err := wal.Write(vote); err != nil {
			t.Fatal(err)
		}
	}
	if err := wal.WriteSync(EndHeightMessage{Height: 1}); err != nil {
		t.Fatal(err)
	}
	return time.Since(start)
}
