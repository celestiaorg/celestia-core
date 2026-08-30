package consensus

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/consensus/propagation"
	"github.com/cometbft/cometbft/crypto"
	cmtrand "github.com/cometbft/cometbft/libs/rand"
	"github.com/cometbft/cometbft/types"
)

type rpConsensusStateCall struct {
	height   int64
	round    int32
	proposer crypto.PubKey
}

type rpEviction struct {
	height int64
	round  int32
}

// recordingPropagator records SetConsensusState and EvictProposal calls.
type recordingPropagator struct {
	*propagation.NoOpPropagator
	mtx       sync.Mutex
	calls     []rpConsensusStateCall
	evictions []rpEviction
}

func newRecordingPropagator() *recordingPropagator {
	return &recordingPropagator{NoOpPropagator: propagation.NewNoOpPropagator()}
}

func (r *recordingPropagator) SetConsensusState(height int64, round int32, proposer crypto.PubKey) {
	r.mtx.Lock()
	defer r.mtx.Unlock()
	r.calls = append(r.calls, rpConsensusStateCall{height, round, proposer})
}

func (r *recordingPropagator) EvictProposal(height int64, round int32) {
	r.mtx.Lock()
	defer r.mtx.Unlock()
	r.evictions = append(r.evictions, rpEviction{height, round})
}

func (r *recordingPropagator) ConsensusStateCalls() []rpConsensusStateCall {
	r.mtx.Lock()
	defer r.mtx.Unlock()
	return append([]rpConsensusStateCall{}, r.calls...)
}

func (r *recordingPropagator) Evictions() []rpEviction {
	r.mtx.Lock()
	defer r.mtx.Unlock()
	return append([]rpEviction{}, r.evictions...)
}

// TestSwitchToConsensusUsesReplayedRoundProposer asserts that propagation is
// initialized from the round state left by consensus WAL replay: at a round
// greater than zero the round-adjusted proposer is installed, not the round-0
// proposer from the committed state.
func TestSwitchToConsensusUsesReplayedRoundProposer(t *testing.T) {
	cs, _ := randState(4)

	// simulate the round state after WAL replay resumed at round 2:
	// enterNewRound has advanced the validator set to the replayed round.
	cs.rsMtx.Lock()
	round0Proposer := cs.rs.Validators.GetProposer().PubKey
	replayedVals := cs.rs.Validators.Copy()
	replayedVals.IncrementProposerPriority(2)
	cs.rs.Round = 2
	cs.rs.Validators = replayedVals
	height := cs.rs.Height
	cs.rsMtx.Unlock()

	replayedProposer := replayedVals.GetProposer().PubKey
	require.False(t, replayedProposer.Equals(round0Proposer),
		"test requires the round-2 proposer to differ from the round-0 proposer")

	prop := newRecordingPropagator()
	conR := NewReactor(cs, prop, false)
	conR.initPropagation()

	calls := prop.ConsensusStateCalls()
	require.Len(t, calls, 1)
	require.Equal(t, height, calls[0].height)
	require.EqualValues(t, 2, calls[0].round)
	require.True(t, calls[0].proposer.Equals(replayedProposer),
		"propagation must start with the round-2 proposer")
	require.False(t, calls[0].proposer.Equals(round0Proposer),
		"propagation must not expose the round-0 proposer for round 2")
}

// TestConsensusRejectionEvictsPropagationProposal asserts that a proposal
// deterministically rejected by consensus is evicted from propagation, while
// routine height or round mismatches are not.
func TestConsensusRejectionEvictsPropagationProposal(t *testing.T) {
	cs, _ := randState(2)
	prop := newRecordingPropagator()
	cs.propagator = prop

	// a proposal with an invalid signature can never be accepted at this
	// height and round.
	blockID := types.BlockID{
		Hash:          cmtrand.Bytes(32),
		PartSetHeader: types.PartSetHeader{Total: 1, Hash: cmtrand.Bytes(32)},
	}
	proposal := types.NewProposal(cs.rs.Height, cs.rs.Round, -1, blockID)
	proposal.Signature = cmtrand.Bytes(64)

	cs.handleMsg(msgInfo{Msg: &ProposalMessage{Proposal: proposal}, PeerID: "peer1"})
	require.Equal(t, []rpEviction{{cs.rs.Height, cs.rs.Round}}, prop.Evictions())

	// a routine height mismatch is not a rejection and must not evict.
	stale := &types.Proposal{Height: cs.rs.Height + 1, Round: cs.rs.Round}
	cs.handleMsg(msgInfo{Msg: &ProposalMessage{Proposal: stale}, PeerID: "peer1"})
	require.Len(t, prop.Evictions(), 1)
}
