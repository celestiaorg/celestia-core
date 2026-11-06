package evidence

import "github.com/cometbft/cometbft/types"

// AddPendingEvidenceUnchecked writes ev to the pending store without
// verification or deduplication, as pools written by older versions may have.
func (evpool *Pool) AddPendingEvidenceUnchecked(ev types.Evidence) error {
	return evpool.addPendingEvidence(ev)
}
