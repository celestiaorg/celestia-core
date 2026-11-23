package types

import (
	"bytes"
	"runtime"
	"sync"
	"sync/atomic"

	"github.com/cometbft/cometbft/crypto"
	"github.com/cometbft/cometbft/crypto/batch"
)

const (
	// parallelCommitVerifyMinSigs is the smallest commit verified in parallel.
	parallelCommitVerifyMinSigs = 16
	// parallelCommitVerifyMinChunk is the smallest number of signatures per goroutine.
	parallelCommitVerifyMinChunk = 8
)

// parallelCommitVerify enables the parallel pass in verifyCommitBatch. The
// node sets it once at start from config; it defaults to true.
var parallelCommitVerify atomic.Bool

// parallelCommitVerifyAccepted counts commits accepted by the parallel pass.
var parallelCommitVerifyAccepted atomic.Int64

// parallelCommitVerifySigs counts signatures handed to the parallel pass.
var parallelCommitVerifySigs atomic.Int64

func init() { parallelCommitVerify.Store(true) }

// SetParallelCommitVerify enables or disables parallel commit verification
// and returns the previous value.
func SetParallelCommitVerify(enabled bool) bool {
	return parallelCommitVerify.Swap(enabled)
}

// tryParallelVerifyCommitBatch batch-verifies the commit in chunks on up to
// GOMAXPROCS goroutines. It returns true only if verifyCommitBatch (all
// signatures, by index) would return nil; a batch accepts iff all its
// signatures are valid, so all chunks accepting equals the whole batch accepting.
func tryParallelVerifyCommitBatch(
	chainID string,
	vals *ValidatorSet,
	commit *Commit,
	votingPowerNeeded int64,
	ignoreSig func(CommitSig) bool,
	countSig func(CommitSig) bool,
) (accepted bool) {
	defer func() {
		if r := recover(); r != nil {
			accepted = false
		}
	}()

	idxs, ok := parallelCommitPrePass(vals, commit, votingPowerNeeded, ignoreSig, countSig)
	if !ok {
		return false
	}

	workers := runtime.GOMAXPROCS(0)
	chunk := (len(idxs) + workers - 1) / workers
	if chunk < parallelCommitVerifyMinChunk {
		chunk = parallelCommitVerifyMinChunk
	}
	proposerKey := vals.GetProposer().PubKey

	var (
		wg     sync.WaitGroup
		failed atomic.Bool
	)
	for start := 0; start < len(idxs); start += chunk {
		part := idxs[start:min(start+chunk, len(idxs))]
		wg.Add(1)
		go func() {
			defer wg.Done()
			if !verifyCommitChunk(chainID, vals, commit, proposerKey, part, &failed) {
				failed.Store(true)
			}
		}()
	}
	wg.Wait()
	return !failed.Load()
}

// parallelCommitPrePass runs the non-signature checks of verifyCommitBatch
// (by index) and returns the indices of the signatures to verify.
func parallelCommitPrePass(
	vals *ValidatorSet,
	commit *Commit,
	votingPowerNeeded int64,
	ignoreSig func(CommitSig) bool,
	countSig func(CommitSig) bool,
) ([]int, bool) {
	n := len(commit.Signatures)
	if n < parallelCommitVerifyMinSigs || len(vals.Validators) != n {
		return nil, false
	}
	idxs := make([]int, 0, n)
	var tallied int64
	for idx, commitSig := range commit.Signatures {
		if ignoreSig(commitSig) {
			continue
		}
		val := vals.Validators[idx]
		if !bytes.Equal(val.Address, commitSig.ValidatorAddress) {
			return nil, false
		}
		idxs = append(idxs, idx)
		if countSig(commitSig) {
			tallied += val.VotingPower
		}
	}
	return idxs, tallied > votingPowerNeeded && len(idxs) > 0
}

// verifyCommitChunk batch-verifies the signatures at idxs. It stops early,
// returning false, once another chunk has failed.
func verifyCommitChunk(
	chainID string,
	vals *ValidatorSet,
	commit *Commit,
	proposerKey crypto.PubKey,
	idxs []int,
	failed *atomic.Bool,
) (ok bool) {
	defer func() {
		if r := recover(); r != nil {
			ok = false
		}
	}()
	bv, supported := batch.CreateBatchVerifier(proposerKey)
	if !supported || failed.Load() {
		return false
	}
	parallelCommitVerifySigs.Add(int64(len(idxs)))
	for _, idx := range idxs {
		signBytes := commit.VoteSignBytes(chainID, int32(idx))
		if err := bv.Add(vals.Validators[idx].PubKey, signBytes, commit.Signatures[idx].Signature); err != nil {
			return false
		}
	}
	if failed.Load() {
		return false
	}
	ok, _ = bv.Verify()
	return ok
}
