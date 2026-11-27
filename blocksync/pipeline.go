package blocksync

import (
	"bytes"

	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/types"
)

// While block H is applied, a goroutine prepares block H+1: its part set and
// block ID, and the verification of its commit (carried in H+2) against
// Validators(H+1). Results are used only for the same block objects and an
// identical validator set, and only on success; otherwise the stock code runs.

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

// blockPartsAndID returns the part set and block ID of b.
func blockPartsAndID(b *types.Block) (*types.PartSet, types.BlockID, error) {
	parts, err := b.MakePartSet(types.BlockPartSizeBytes)
	if err != nil {
		return nil, types.BlockID{}, err
	}
	return parts, types.BlockID{Hash: b.Hash(), PartSetHeader: parts.Header()}, nil
}

// startBlockSpec launches the speculative computation for block (H+1) using
// next (H+2, may be nil) and vals, which must be a private copy owned by the
// spec goroutine.
func startBlockSpec(chainID string, block, next *types.Block, vals *types.ValidatorSet, logger log.Logger) *blockSpec {
	s := &blockSpec{block: block, next: next, vals: vals, done: make(chan struct{})}
	go s.run(chainID, logger)
	return s
}

func (s *blockSpec) run(chainID string, logger log.Logger) {
	defer close(s.done)
	defer func() {
		// A panic here only discards the speculation; the stock code then
		// processes the block on the main goroutine.
		if r := recover(); r != nil {
			logger.Error("blocksync pipeline panicked", "height", s.block.Height, "err", r)
			s.partsOK, s.commitOK = false, false
		}
	}()
	var err error
	if s.parts, s.blockID, err = blockPartsAndID(s.block); err != nil {
		return
	}
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

// commitVerified reports whether second.LastCommit was successfully verified
// for first against a validator set with the same hash as vals.
func (s *blockSpec) commitVerified(first, second *types.Block, vals *types.ValidatorSet) bool {
	return s != nil && s.next != nil && s.block == first && s.next == second &&
		s.partsOK && s.commitOK && vals != nil &&
		len(s.valsHash) > 0 && bytes.Equal(s.valsHash, vals.Hash())
}
