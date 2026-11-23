package types

import (
	"bytes"
	"runtime"
	"sync"
	"sync/atomic"

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

	n := len(commit.Signatures)
	if n < parallelCommitVerifyMinSigs || len(vals.Validators) != n {
		return false
	}

	// Sequential cheap pre-pass mirroring the checks of verifyCommitBatch
	// (lookUpByIndex=true) except the signature work.
	idxs := make([]int, 0, n)
	var tallied int64
	for idx, commitSig := range commit.Signatures {
		if ignoreSig(commitSig) {
			continue
		}
		val := vals.Validators[idx]
		if !bytes.Equal(val.Address, commitSig.ValidatorAddress) {
			return false
		}
		idxs = append(idxs, idx)
		if countSig(commitSig) {
			tallied += val.VotingPower
		}
	}
	if tallied <= votingPowerNeeded || len(idxs) == 0 {
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
		failed = make(chan struct{}, (len(idxs)+chunk-1)/chunk)
	)
	for start := 0; start < len(idxs); start += chunk {
		end := start + chunk
		if end > len(idxs) {
			end = len(idxs)
		}
		part := idxs[start:end]
		wg.Add(1)
		go func(part []int) {
			defer wg.Done()
			ok := false
			defer func() {
				if r := recover(); r != nil {
					ok = false
				}
				if !ok {
					failed <- struct{}{}
				}
			}()
			bv, supported := batch.CreateBatchVerifier(proposerKey)
			if !supported {
				return
			}
			for _, idx := range part {
				val := vals.Validators[idx]
				signBytes := commit.VoteSignBytes(chainID, int32(idx))
				if err := bv.Add(val.PubKey, signBytes, commit.Signatures[idx].Signature); err != nil {
					return
				}
			}
			ok, _ = bv.Verify()
		}(part)
	}
	wg.Wait()
	close(failed)
	for range failed {
		return false
	}
	return true
}
