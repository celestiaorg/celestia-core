package evidence

import (
	"github.com/cosmos/gogoproto/proto"
	gogotypes "github.com/cosmos/gogoproto/types"

	cmttypes "github.com/cometbft/cometbft/types"
)

// AddPendingEvidenceUnchecked writes ev to the pending store without
// verification or deduplication, as pools written by older versions may have.
func (evpool *Pool) AddPendingEvidenceUnchecked(ev cmttypes.Evidence) error {
	return evpool.addPendingEvidence(ev)
}

// MarkCommittedWithoutDuplicateVoteMarker records ev as committed by hash
// only, as released versions do.
func (evpool *Pool) MarkCommittedWithoutDuplicateVoteMarker(ev cmttypes.Evidence) error {
	h, err := proto.Marshal(&gogotypes.Int64Value{Value: ev.Height()})
	if err != nil {
		return err
	}
	return evpool.evidenceStore.Set(keyCommitted(ev), h)
}
