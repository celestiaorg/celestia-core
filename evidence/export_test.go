package evidence

import "github.com/cometbft/cometbft/types"

// AddPendingEvidenceUnchecked writes ev to the pending store without
// verification or deduplication, as pools written by older versions may have.
func (evpool *Pool) AddPendingEvidenceUnchecked(ev types.Evidence) error {
	return evpool.addPendingEvidence(ev)
}

// HasCommittedDuplicateVote reports whether the pool has recorded that
// duplicate vote evidence for dve's validator and height was committed.
func (evpool *Pool) HasCommittedDuplicateVote(dve *types.DuplicateVoteEvidence) bool {
	ok, err := evpool.evidenceStore.Has(keyCommittedDuplicateVote(dve))
	return err == nil && ok
}

// MarkCommittedWithoutDuplicateVoteMarker records ev as committed the way
// pools written before duplicate vote commit markers did.
func (evpool *Pool) MarkCommittedWithoutDuplicateVoteMarker(ev types.Evidence) error {
	return evpool.evidenceStore.Set(keyCommitted(ev), []byte{})
}

// AddEvidenceUnchecked adds ev to the pending store and to the gossip list
// without verification or deduplication, as an older version may have.
func (evpool *Pool) AddEvidenceUnchecked(ev types.Evidence) error {
	if err := evpool.addPendingEvidence(ev); err != nil {
		return err
	}
	evpool.evidenceList.PushBack(ev)
	return nil
}
