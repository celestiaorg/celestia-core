package consensus

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/consensus/propagation"
	cmtrand "github.com/cometbft/cometbft/libs/rand"
	"github.com/cometbft/cometbft/types"
)

// recordingPropagator records the proposals evicted by consensus.
type recordingPropagator struct {
	*propagation.NoOpPropagator
	mtx       sync.Mutex
	evictions []types.BlockID
}

func newRecordingPropagator() *recordingPropagator {
	return &recordingPropagator{NoOpPropagator: propagation.NewNoOpPropagator()}
}

func (r *recordingPropagator) EvictProposal(_ int64, _ int32, blockID types.BlockID) {
	r.mtx.Lock()
	defer r.mtx.Unlock()
	r.evictions = append(r.evictions, blockID)
}

func (r *recordingPropagator) Evictions() []types.BlockID {
	r.mtx.Lock()
	defer r.mtx.Unlock()
	return append([]types.BlockID{}, r.evictions...)
}

// TestConsensusRejectionEvictsProposal asserts that a proposal consensus
// deterministically rejects is evicted from propagation, and that a routine
// height mismatch is not treated as a rejection.
func TestConsensusRejectionEvictsProposal(t *testing.T) {
	cs, _ := randState(2)
	prop := newRecordingPropagator()
	cs.propagator = prop

	blockID := types.BlockID{
		Hash:          cmtrand.Bytes(32),
		PartSetHeader: types.PartSetHeader{Total: 1, Hash: cmtrand.Bytes(32)},
	}
	// an invalid signature can never be accepted at this height and round.
	proposal := types.NewProposal(cs.rs.Height, cs.rs.Round, -1, blockID)
	proposal.Signature = cmtrand.Bytes(64)

	cs.handleMsg(msgInfo{Msg: &ProposalMessage{Proposal: proposal}, PeerID: "peer1"})
	require.Equal(t, []types.BlockID{blockID}, prop.Evictions())

	// a proposal for a height we have not reached is routine and must not evict.
	stale := types.NewProposal(cs.rs.Height+1, cs.rs.Round, -1, blockID)
	stale.Signature = cmtrand.Bytes(64)
	cs.handleMsg(msgInfo{Msg: &ProposalMessage{Proposal: stale}, PeerID: "peer1"})
	require.Len(t, prop.Evictions(), 1)
}
