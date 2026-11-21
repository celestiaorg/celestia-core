package blocksync

import (
	"bytes"
	"os"
	"sync/atomic"

	"github.com/cometbft/cometbft/types"
)

// Blocksync pipelining (celestia prototype).
//
// Enabled with CELESTIA_BLOCKSYNC_PIPELINE=1. While block H is being applied
// (ApplyVerifiedBlock -> app FinalizeBlock), a goroutine precomputes for
// block H+1 the work that does not depend on H's execution result:
//
//   - MakePartSet(H+1) and the BlockID of H+1 (Hash + PartSetHeader),
//   - VerifyCommit of H+2.LastCommit (the commit for H+1) against the
//     validator set expected for H+1, which is state.NextValidators as of
//     before applying H (updateState sets Validators(H+1) = NextValidators).
//
// When iteration H+1 starts, the speculative result is used only if:
//
//   - the pool still returns the very same *types.Block pointers for H+1
//     (and H+2 for the commit result); blocks are immutable once received and
//     a re-requested block is always a new object,
//   - the speculative validator set has the same Hash() as state.Validators
//     and the same (address, pubkey, voting power) at every index,
//   - the speculative result was a success (MakePartSet without error,
//     VerifyCommit == nil). A speculative error is never surfaced: the
//     original code is re-run synchronously and produces the error.
//
// Otherwise the original sequential code runs unchanged.

var blocksyncPipelineEnv = os.Getenv("CELESTIA_BLOCKSYNC_PIPELINE") == "1"

// pipelineStats counts speculative outcomes (diagnostics/tests).
type pipelineStats struct {
	started        atomic.Int64
	partsUsed      atomic.Int64
	commitUsed     atomic.Int64
	commitMissed   atomic.Int64
	partsDiscarded atomic.Int64
	// breakdown of commitMissed
	missNoNext       atomic.Int64 // H+2 was not available when speculating
	missBlockChanged atomic.Int64 // pool returned different block objects
	missSpecFailed   atomic.Int64 // speculative verification did not succeed
	missValsMismatch atomic.Int64 // speculated validator set != state.Validators
}

type blockSpec struct {
	block *types.Block // H+1
	next  *types.Block // H+2, may be nil
	vals  *types.ValidatorSet
	done  chan struct{}

	// results, valid after done is closed
	parts    *types.PartSet
	blockID  types.BlockID
	partsOK  bool
	valsHash []byte
	commitOK bool
}

// canSpeculate reports whether speculative work on b is free of writes to
// fields that the main goroutine may read concurrently. Block.Hash() calls
// fillHeader, which writes memoized hashes only when these are nil.
func canSpeculate(b *types.Block) bool {
	return b != nil && b.LastCommit != nil &&
		b.LastCommitHash != nil && b.DataHash != nil && b.EvidenceHash != nil
}

// startBlockSpec launches the speculative computation for block (H+1) using
// next (H+2, may be nil) and vals, which must be a private copy owned by the
// spec goroutine.
func startBlockSpec(chainID string, block, next *types.Block, vals *types.ValidatorSet) *blockSpec {
	s := &blockSpec{block: block, next: next, vals: vals, done: make(chan struct{})}
	go s.run(chainID)
	return s
}

func (s *blockSpec) run(chainID string) {
	defer close(s.done)
	defer func() {
		if r := recover(); r != nil {
			s.partsOK = false
			s.commitOK = false
		}
	}()
	parts, err := s.block.MakePartSet(types.BlockPartSizeBytes)
	if err != nil || parts == nil {
		return
	}
	s.parts = parts
	s.blockID = types.BlockID{Hash: s.block.Hash(), PartSetHeader: parts.Header()}
	s.partsOK = true

	if s.next == nil || s.vals == nil {
		return
	}
	s.valsHash = s.vals.Hash()
	s.commitOK = s.vals.VerifyCommit(chainID, s.blockID, s.block.Height, s.next.LastCommit) == nil
}

// partsFor returns the precomputed part set and block ID if they were
// computed for exactly this block object.
func (s *blockSpec) partsFor(first *types.Block) (*types.PartSet, types.BlockID, bool) {
	if s == nil || s.block != first || !s.partsOK {
		return nil, types.BlockID{}, false
	}
	return s.parts, s.blockID, true
}

// commitVerifiedFor reports whether second.LastCommit was successfully
// verified for first against a validator set identical to vals.
func (s *blockSpec) commitVerifiedFor(first, second *types.Block, vals *types.ValidatorSet) bool {
	return s.commitCheck(first, second, vals, nil)
}

func (s *blockSpec) commitCheck(first, second *types.Block, vals *types.ValidatorSet, st *pipelineStats) bool {
	if st == nil {
		st = &pipelineStats{} // discard counters
	}
	miss := func(c *atomic.Int64) bool {
		c.Add(1)
		return false
	}
	switch {
	case s == nil:
		return false
	case s.next == nil:
		return miss(&st.missNoNext)
	case s.block != first || s.next != second:
		return miss(&st.missBlockChanged)
	case !s.partsOK || !s.commitOK:
		return miss(&st.missSpecFailed)
	case !sameValidatorSet(s.vals, s.valsHash, vals):
		return miss(&st.missValsMismatch)
	}
	return true
}

// sameValidatorSet checks that two validator sets agree on everything
// VerifyCommit reads: size, order, address, pubkey and voting power (and
// hence total power and key types), plus Hash() equality.
func sameValidatorSet(spec *types.ValidatorSet, specHash []byte, cur *types.ValidatorSet) bool {
	if spec == nil || cur == nil || len(spec.Validators) != len(cur.Validators) {
		return false
	}
	for i, a := range spec.Validators {
		b := cur.Validators[i]
		if a == nil || b == nil || a.PubKey == nil || b.PubKey == nil {
			return false
		}
		if !bytes.Equal(a.Address, b.Address) || a.VotingPower != b.VotingPower || !a.PubKey.Equals(b.PubKey) {
			return false
		}
	}
	return len(specHash) > 0 && bytes.Equal(specHash, cur.Hash())
}
