package consensus

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/types"
)

// TestPartBeforeProposalIsRefetched checks that a block part handled before its
// proposal is not lost. syncData reads the proposal and part channels in one
// select, so the propagator's parts can reach the state before the proposal.
// addProposalBlockPart drops them, and the propagator, which holds the complete
// block, does not send them again. Setting the proposal must pull the held parts
// so the node prevotes the block instead of nil.
func TestPartBeforeProposalIsRefetched(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cs1, vss := randState(2)
	vs2 := vss[1]
	height := cs1.rs.Height

	// vs2 proposes round 1.
	round := int32(1)
	incrementRound(vss[1:]...)
	proposal, block := decideProposal(ctx, t, cs1, vs2, vs2.Height, vs2.Round)
	parts, err := block.MakePartSet(types.BlockPartSizeBytes)
	require.NoError(t, err)

	// The propagator already holds the complete block for round 1.
	feed := &lockRoundFeeder{Propagator: cs1.propagator, partChan: make(chan types.PartInfo, parts.Total())}
	feed.deliver(round, parts, false)
	cs1.propagator = feed

	voteCh := subscribe(cs1.eventBus, types.EventQueryVote)
	startTestRound(cs1, height, round)
	// startTestRound does not start syncData, which passes on the propagator's parts.
	go cs1.syncData()

	// The parts reach the state before the proposal, as syncData can forward them.
	for i := 0; i < int(parts.Total()); i++ {
		require.NoError(t, cs1.AddProposalBlockPart(height, round, parts.GetPart(i), "peer"))
	}
	require.NoError(t, cs1.SetProposal(proposal, "peer"))

	ensurePrevoteMatch(t, voteCh, height, round, block.Hash())
}
