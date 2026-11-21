package types

import (
	"bytes"
	"os"
	"runtime"
	"sync"
	"sync/atomic"

	"github.com/cometbft/cometbft/crypto/batch"
)

// Parallel commit verification (celestia prototype).
//
// Enabled with CELESTIA_PARALLEL_COMMIT_VERIFY=1. When enabled,
// verifyCommitBatch first runs an "accept-only" parallel pass: the
// signatures of the commit are split into chunks and every chunk is
// verified with its own ed25519 BatchVerifier on its own goroutine
// (including the VoteSignBytes construction and BatchVerifier.Add, which
// performs point decompression and hashing).
//
// The parallel pass can only ever return "accepted". If every check that the
// sequential code performs passes (address match by index, Add success,
// sufficient voting power, every chunk's batch verifies), the commit is
// accepted and nil is returned, exactly as the sequential code would.
// In every other case (any failure, panic, or unsupported input) the
// parallel result is discarded and the original sequential code runs
// unchanged, so the returned error (type, message, precedence and
// early-return behaviour) is by construction identical to the original.
//
// Batch verification of a set of signatures accepts iff every signature in
// it is individually valid (ZIP-215 cofactored verification, up to the
// same negligible soundness error that the original single batch already
// has), so "every chunk accepts" is equivalent to "the whole batch accepts".

const (
	// minimum number of signatures in a commit before the parallel path is used.
	parallelCommitVerifyMinSigs = 16
	// minimum number of signatures per chunk; keeps batch verification efficient.
	parallelCommitVerifyMinChunk = 8
)

var parallelCommitVerifyEnabled atomic.Bool

func init() {
	parallelCommitVerifyEnabled.Store(os.Getenv("CELESTIA_PARALLEL_COMMIT_VERIFY") == "1")
}

// SetParallelCommitVerify enables or disables the parallel commit verification
// prototype at runtime and returns the previous value. Intended for tests.
func SetParallelCommitVerify(enabled bool) bool {
	return parallelCommitVerifyEnabled.Swap(enabled)
}

// ParallelCommitVerifyEnabled reports whether the parallel commit
// verification prototype is enabled.
func ParallelCommitVerifyEnabled() bool { return parallelCommitVerifyEnabled.Load() }

// parallelCommitVerifyStats counts parallel-path outcomes (for tests/diagnostics).
var parallelCommitVerifyStats struct {
	sync.Mutex
	accepted, fellBack int
}

func recordParallelOutcome(accepted bool) {
	parallelCommitVerifyStats.Lock()
	if accepted {
		parallelCommitVerifyStats.accepted++
	} else {
		parallelCommitVerifyStats.fellBack++
	}
	parallelCommitVerifyStats.Unlock()
}

// tryParallelVerifyCommitBatch returns true only if the commit would be
// accepted (nil error) by verifyCommitBatch with countAllSignatures=true and
// lookUpByIndex=true. A false return carries no information; the caller
// must then run the original sequential code.
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
