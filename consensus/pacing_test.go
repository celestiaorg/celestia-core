package consensus

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/consensus/propagation"
	cstypes "github.com/cometbft/cometbft/consensus/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cometbft/cometbft/types"
)

func TestPacingCompleteProposalWaitsForHeightStart(t *testing.T) {
	cs, _ := randState(4)
	t.Cleanup(func() { require.NoError(t, cs.eventBus.Stop()) })
	block, parts, err := cs.createProposalBlock(context.Background())
	require.NoError(t, err)
	cs.rs.Proposal = types.NewProposal(cs.rs.Height, 0, -1,
		types.BlockID{Hash: block.Hash(), PartSetHeader: parts.Header()})
	cs.rs.ProposalBlock = block
	cs.rs.ProposalBlockParts = parts
	cs.rs.StartTime = time.Now().Add(time.Hour)
	cs.rs.Step = cstypes.RoundStepNewHeight
	prevoted := false
	cs.doPrevote = func(int64, int32) { prevoted = true }
	cs.decideProposal = func(int64, int32) {}

	cs.handleCompleteProposal(cs.rs.Height)
	require.False(t, prevoted, "an early proposal must not bypass the height's pacing deadline")
	require.Equal(t, cstypes.RoundStepNewHeight, cs.rs.Step)

	cs.rs.StartTime = time.Now().Add(-time.Second)
	cs.enterNewRound(cs.rs.Height, 0)
	require.True(t, prevoted, "the cached proposal must be processed when the height starts")
	require.Equal(t, cstypes.RoundStepPrevote, cs.rs.Step)
}

func TestPacingEarlyRoundZeroWaitsForHeightStart(t *testing.T) {
	cs, _ := randState(4)
	t.Cleanup(func() { require.NoError(t, cs.eventBus.Stop()) })
	cs.SetPrivValidator(nil)
	cs.rs.StartTime = time.Now().Add(time.Hour)

	cs.enterNewRound(cs.rs.Height, 0)

	require.Equal(t, cstypes.RoundStepNewHeight, cs.rs.Step,
		"enterNewRound must preserve the pacing deadline when called before its timeout")
}

func TestPacingUsesLocalStartAcrossValidatorClockOffsets(t *testing.T) {
	base := time.Now().Add(time.Hour)
	for _, offset := range []time.Duration{-2 * time.Second, 0, 2 * time.Second} {
		t.Run(offset.String(), func(t *testing.T) {
			localStart := base.Add(offset)
			localCommit := localStart.Add(400 * time.Millisecond)
			// Every validator receives the same signed proposer timestamp. Its
			// own clock offset must not change the elapsed pacing interval.
			cs := pacingCommittedState(t, localStart, localCommit, base.Add(300*time.Millisecond))
			nextState := cs.state.Copy()
			nextState.LastBlockHeight = cs.rs.Height
			nextState.LastValidators = cs.rs.Validators

			cs.updateToState(nextState)

			require.Equal(t, time.Second, cs.rs.StartTime.Sub(localStart),
				"proposal construction and a remote validator's clock must not alter the height interval")
		})
	}
}

func TestPacingSlowFinalizationDoesNotLeaveStaleStart(t *testing.T) {
	start := time.Now().Add(-5 * time.Second)
	cs := pacingCommittedState(t, start, start.Add(100*time.Millisecond), start)
	nextState := cs.state.Copy()
	nextState.LastBlockHeight = cs.rs.Height
	nextState.LastValidators = cs.rs.Validators
	finalizationComplete := time.Now()

	cs.updateToState(nextState)

	require.False(t, cs.rs.StartTime.Before(finalizationComplete),
		"a long FinalizeBlock must not leave a stale anchor that lets the following height run early")
}

func TestPacingCommitBeforeScheduledStartDoesNotAccumulateDelay(t *testing.T) {
	now := time.Now()
	cs := pacingCommittedState(t, now.Add(10*time.Second), now, now.Add(10*time.Second))
	nextState := cs.state.Copy()
	nextState.LastBlockHeight = cs.rs.Height
	nextState.LastValidators = cs.rs.Validators

	cs.updateToState(nextState)

	require.False(t, cs.rs.StartTime.After(time.Now().Add(time.Second)),
		"committing before a scheduled start must not carry an unused deadline into later heights")
}

func TestPacingFirstCommitAlignsLocalStartupTimes(t *testing.T) {
	commit := time.Now().Add(time.Hour)
	for _, offset := range []time.Duration{-2 * time.Second, -200 * time.Millisecond, 200 * time.Millisecond} {
		t.Run(offset.String(), func(t *testing.T) {
			cs := pacingCommittedState(t, commit.Add(offset), commit, commit.Add(-time.Hour))
			cs.pacingStarted = false
			nextState := cs.state.Copy()
			nextState.LastBlockHeight = cs.rs.Height
			nextState.LastValidators = cs.rs.Validators

			cs.updateToState(nextState)

			require.Equal(t, time.Second, cs.rs.StartTime.Sub(commit),
				"startup cadence must anchor on a common observed event, not each validator's startup time")
		})
	}
}

func TestPacingCatchupResetsCadenceWithoutWaiting(t *testing.T) {
	now := time.Now()
	cs := pacingCommittedState(t, now.Add(-200*time.Millisecond), now, now)
	cs.propagator = pacingBehindPropagator{Propagator: cs.propagator}
	nextState := cs.state.Copy()
	nextState.LastBlockHeight = cs.rs.Height
	nextState.LastValidators = cs.rs.Validators

	cs.updateToState(nextState)

	require.False(t, cs.rs.StartTime.After(time.Now()), "catch-up must not wait for live block pacing")
	require.False(t, cs.pacingStarted, "the first live commit after catch-up must establish a fresh cadence")
}

type pacingBehindPropagator struct{ propagation.Propagator }

func (pacingBehindPropagator) IsBehind() bool { return true }

func TestPacingAllLastCommitVotesDoNotSkipHeightStart(t *testing.T) {
	cs, vss := randState(4)
	t.Cleanup(func() { require.NoError(t, cs.eventBus.Stop()) })
	block, parts, err := cs.createProposalBlock(context.Background())
	require.NoError(t, err)
	for _, vote := range signVotes(cmtproto.PrecommitType, block.Hash(), parts.Header(), true, vss[1:]...) {
		added, err := cs.rs.Votes.AddVote(vote, "peer", true)
		require.NoError(t, err)
		require.True(t, added)
	}
	vss[0].Height = cs.rs.Height
	vote, err := vss[0].signVote(cmtproto.PrecommitType, block.Hash(), parts.Header(), nil, true)
	require.NoError(t, err)
	cs.rs.LastCommit = cs.rs.Votes.Precommits(0)
	cs.rs.Height++
	cs.rs.Votes = cstypes.NewHeightVoteSet(cs.state.ChainID, cs.rs.Height, cs.rs.Validators)
	cs.rs.StartTime = time.Now().Add(time.Hour)
	cs.rs.Step = cstypes.RoundStepNewHeight
	cs.config.SkipTimeoutCommit = true
	cs.SetPrivValidator(nil)

	added, err := cs.addVote(vote, "peer")

	require.NoError(t, err)
	require.True(t, added)
	require.True(t, cs.rs.LastCommit.HasAll())
	require.Equal(t, cstypes.RoundStepNewHeight, cs.rs.Step,
		"collecting all previous precommits must not bypass the block interval")
}

func pacingCommittedState(t *testing.T, start, commit, proposalTime time.Time) *State {
	t.Helper()
	cs, vss := randState(4)
	t.Cleanup(func() { require.NoError(t, cs.eventBus.Stop()) })
	cs.state.Timeouts.TimeoutCommit = time.Second
	cs.config.TimeoutCommit = time.Second
	cs.pacingStarted = true
	block, parts, err := cs.createProposalBlock(context.Background())
	require.NoError(t, err)
	blockID := types.BlockID{Hash: block.Hash(), PartSetHeader: parts.Header()}
	proposal := types.NewProposal(cs.rs.Height, 0, -1, blockID)
	proposal.Timestamp = proposalTime
	p := proposal.ToProto()
	require.NoError(t, cs.privValidator.SignProposal(cs.state.ChainID, p))
	proposal.Signature = p.Signature
	require.NoError(t, cs.defaultSetProposal(proposal))
	for _, vote := range signVotes(cmtproto.PrecommitType, blockID.Hash, blockID.PartSetHeader, true, vss[1:]...) {
		added, err := cs.rs.Votes.AddVote(vote, "peer", true)
		require.NoError(t, err)
		require.True(t, added)
	}
	cs.rs.StartTime = start
	cs.rs.CommitTime = commit
	cs.rs.CommitRound = 0
	return cs
}
