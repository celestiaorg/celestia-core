package consensus

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/go-kit/kit/metrics"
	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/abci/example/kvstore"
	"github.com/cometbft/cometbft/consensus/propagation"
	cstypes "github.com/cometbft/cometbft/consensus/types"
	"github.com/cometbft/cometbft/crypto/tmhash"
	"github.com/cometbft/cometbft/internal/test"
	cmtrand "github.com/cometbft/cometbft/libs/rand"
	cmtsync "github.com/cometbft/cometbft/libs/sync"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	sm "github.com/cometbft/cometbft/state"
	"github.com/cometbft/cometbft/types"
)

const testDynamicTimeoutCommit = time.Second

func pacingRandState(t *testing.T) (*State, []*validatorStub) {
	t.Helper()
	cs, vss := randState(4)
	t.Cleanup(func() { require.NoError(t, cs.eventBus.Stop()) })
	return cs, vss
}

// pacedState returns a node mid-cadence from start whose app sets
// DynamicTimeoutCommit and a 100ms TimeoutCommit, as celestia-app v11 does.
func pacedState(t *testing.T, start time.Time) (*State, []*validatorStub) {
	t.Helper()
	cs, vss := pacingRandState(t)
	cs.state.Timeouts.TimeoutCommit = 100 * time.Millisecond
	cs.state.Timeouts.DynamicTimeoutCommit = testDynamicTimeoutCommit
	cs.pacingStarted = true
	cs.rs.StartTime = start
	return cs, vss
}

// pacingCompleteProposal installs a complete round 0 proposal and stubs the
// node's own proposal; the returned flag records whether it prevoted.
func pacingCompleteProposal(t *testing.T, cs *State) *bool {
	t.Helper()
	block, parts, err := cs.createProposalBlock(context.Background())
	require.NoError(t, err)
	cs.rs.Proposal = types.NewProposal(cs.rs.Height, 0, -1,
		types.BlockID{Hash: block.Hash(), PartSetHeader: parts.Header()})
	cs.rs.ProposalBlock = block
	cs.rs.ProposalBlockParts = parts
	cs.decideProposal = func(int64, int32) {}
	prevoted := false
	cs.doPrevote = func(int64, int32) { prevoted = true }
	return &prevoted
}

// pacingWaitForTxs sets create_empty_blocks=false with no proof block due, so round 0 waits for txs.
func pacingWaitForTxs(t *testing.T, cs *State) {
	t.Helper()
	cs.config.CreateEmptyBlocks = false
	cs.state.InitialHeight = cs.rs.Height + 100
	cs.blockStore = pacingNoProofStore{BlockStore: cs.blockStore, appHash: cs.state.AppHash}
	require.False(t, cs.needProofBlock(cs.rs.Height))
}

func TestPacingCompleteProposalWaitsForHeightStart(t *testing.T) {
	for name, waitForTxs := range map[string]bool{"create empty blocks": false, "wait for txs": true} {
		t.Run(name, func(t *testing.T) {
			cs, _ := pacedState(t, time.Now())
			prevoted := pacingCompleteProposal(t, cs)
			if waitForTxs {
				pacingWaitForTxs(t, cs)
			}
			cs.rs.StartTime = time.Now().Add(time.Hour)

			cs.handleCompleteProposal(cs.rs.Height)
			require.False(t, *prevoted, "an early proposal must not bypass the height's pacing deadline")
			require.Equal(t, cstypes.RoundStepNewHeight, cs.rs.Step)

			cs.rs.StartTime = time.Now().Add(-time.Second)
			cs.enterNewRound(cs.rs.Height, 0)
			require.True(t, *prevoted, "the held proposal must be processed, without waiting for txs, at the height start")
			require.Equal(t, cstypes.RoundStepPrevote, cs.rs.Step)
		})
	}
}

func TestPacingAnchorsOnEarlierOfOwnStartAndCappedProposal(t *testing.T) {
	const ms = time.Millisecond
	start := time.Now().Add(time.Hour)
	// Offsets from the own start; D = 1s, so a pull is capped at 500 ms and legacy = commit + 100 ms.
	for name, tc := range map[string]struct{ propTs, commit, want time.Duration }{
		"own proposal":                     {propTs: 5 * ms, commit: 400 * ms, want: time.Second},
		"same instant":                     {propTs: 0, commit: 400 * ms, want: time.Second},
		"proposer started later":           {propTs: 300 * ms, commit: 400 * ms, want: time.Second},
		"stamped after the commit":         {propTs: 450 * ms, commit: 400 * ms, want: time.Second},
		"stamped after own floor":          {propTs: 2 * time.Second, commit: 400 * ms, want: time.Second},
		"1ns earlier":                      {propTs: -time.Nanosecond, commit: 400 * ms, want: time.Second - time.Nanosecond},
		"proposer clock 50ms behind":       {propTs: -40 * ms, commit: 400 * ms, want: 960 * ms},
		"proposer clock 300ms behind":      {propTs: -290 * ms, commit: 400 * ms, want: 710 * ms},
		"pull bounded by legacy start":     {propTs: -290 * ms, commit: 750 * ms, want: 850 * ms},
		"proposer clock 2s behind: capped": {propTs: -1990 * ms, commit: 200 * ms, want: 500 * ms},
		"backdated 10s: capped":            {propTs: -10 * time.Second, commit: 200 * ms, want: 500 * ms},
		"backdated 10s: legacy start wins": {propTs: -10 * time.Second, commit: 450 * ms, want: 550 * ms},
	} {
		t.Run(name, func(t *testing.T) {
			cs, vss := pacedState(t, start)
			m := recordPacingMetrics(cs)

			pacingCommitProposal(t, cs, vss, start.Add(tc.commit), start.Add(tc.propTs))

			require.Equal(t, tc.want, cs.rs.StartTime.Sub(start))
			var pulls float64
			if tc.propTs < 0 {
				pulls = 1
			}
			m.requireCounts(t, pulls, 0, 0, 1)
		})
	}
}

func TestPacingLaggingNodeReturnsToProposerGrid(t *testing.T) {
	const ms = time.Millisecond
	const prepare = 10 * ms
	// Own-start offsets from the grid after each height: a pull moves at most D/2 = 500 ms.
	for lag, offsets := range map[time.Duration][]time.Duration{
		300 * ms: {prepare, prepare, prepare},
		540 * ms: {40 * ms, prepare, prepare},
		800 * ms: {300 * ms, prepare, prepare},
	} {
		t.Run(lag.String(), func(t *testing.T) {
			grid := time.Now().Add(time.Hour)
			// The node started lag after the grid, e.g. after catching up.
			cs, vss := pacedState(t, grid.Add(lag))
			m := recordPacingMetrics(cs)
			offset, pulls := lag, 0.0
			for h, want := range offsets {
				gridStart := grid.Add(time.Duration(h) * testDynamicTimeoutCommit)
				pacingCommitProposal(t, cs, vss, gridStart.Add(900*ms), gridStart.Add(prepare))
				if offset > prepare {
					pulls++
				}
				offset = want

				require.Equal(t, gridStart.Add(testDynamicTimeoutCommit+want), cs.rs.StartTime,
					"height %d: the node must start one floor after the proposer's timestamp", h)
				m.requireCounts(t, pulls, 0, 0, h+1)
			}
		})
	}
}

func TestPacingPulledDeadlineInPastStartsNow(t *testing.T) {
	// The own floor would still hold for 300 ms, but the capped and legacy deadlines have passed.
	start := time.Now().Add(-700 * time.Millisecond)
	cs, vss := pacedState(t, start)
	m := recordPacingMetrics(cs)
	before := time.Now()

	pacingCommitProposal(t, cs, vss, start.Add(200*time.Millisecond), start.Add(-10*time.Second))

	require.False(t, cs.rs.StartTime.Before(before))
	require.False(t, cs.rs.StartTime.After(time.Now()), "the start must be now, not a past deadline")
	m.requireCounts(t, 1, 1, 0, 1)
}

func TestPacingNoPullWithoutCommittedProposal(t *testing.T) {
	start := time.Now().Add(time.Hour)
	commit := start.Add(400 * time.Millisecond)
	propTs := start.Add(-300 * time.Millisecond)
	for name, commitHeight := range map[string]func(*testing.T, *State, []*validatorStub){
		"no proposal": func(t *testing.T, cs *State, vss []*validatorStub) {
			pacingCommitHeight(t, cs, vss, 0, commit)
		},
		"other block": func(t *testing.T, cs *State, vss []*validatorStub) {
			cs.rs.Proposal = pacingProposal(cs, pacingRandBlockID(), propTs)
			pacingCommitHeight(t, cs, vss, 0, commit)
		},
		"zero block IDs": func(t *testing.T, cs *State, vss []*validatorStub) {
			cs.rs.Proposal = pacingProposal(cs, types.BlockID{}, propTs)
			pacingCommitBlock(t, cs, vss, 0, commit, types.BlockID{})
		},
		"replay": func(t *testing.T, cs *State, vss []*validatorStub) {
			cs.replayMode = true
			pacingCommitProposal(t, cs, vss, commit, propTs)
		},
	} {
		t.Run(name, func(t *testing.T) {
			cs, vss := pacedState(t, start)
			m := recordPacingMetrics(cs)

			commitHeight(t, cs, vss)

			require.Equal(t, start.Add(testDynamicTimeoutCommit), cs.rs.StartTime,
				"only the committed proposal's timestamp, outside replay, may move the deadline")
			if name == "replay" {
				m.requireCounts(t, 0, 0, 0, 0)
			} else {
				m.requireCounts(t, 0, 0, 0, 1)
			}
		})
	}
}

func TestPacingBootstrapIgnoresProposalTimestamp(t *testing.T) {
	commit := time.Now().Add(time.Hour)
	// Both timestamps are before the own start, so a paced height would pull.
	for name, tc := range map[string]struct {
		start, propTs time.Duration
		pacingStarted bool
	}{
		"first commit after restart": {start: -5 * time.Second, propTs: -5300 * time.Millisecond},
		"commit before own start":    {start: 200 * time.Millisecond, propTs: -300 * time.Millisecond, pacingStarted: true},
	} {
		t.Run(name, func(t *testing.T) {
			cs, vss := pacedState(t, commit.Add(tc.start))
			cs.pacingStarted = tc.pacingStarted
			m := recordPacingMetrics(cs)

			pacingCommitProposal(t, cs, vss, commit, commit.Add(tc.propTs))

			require.Equal(t, commit.Add(cs.state.Timeouts.TimeoutCommit), cs.rs.StartTime,
				"a bootstrap height keeps the legacy start whatever the proposal timestamp")
			m.requireCounts(t, 0, 0, 1, 0)
		})
	}
}

func TestPacingSlowFinalizationDoesNotLeaveStaleStart(t *testing.T) {
	start := time.Now().Add(-5 * time.Second)
	cs, vss := pacedState(t, start)
	m := recordPacingMetrics(cs)
	finalizationComplete := time.Now()

	pacingCommitHeight(t, cs, vss, 0, start.Add(100*time.Millisecond))

	require.False(t, cs.rs.StartTime.Before(finalizationComplete),
		"a long FinalizeBlock must not leave a stale anchor that lets the following height run early")
	m.requireCounts(t, 0, 1, 0, 1)
}

func TestPacingFirstCommitAlignsLocalStartupTimes(t *testing.T) {
	commit := time.Now().Add(time.Hour)
	for _, offset := range []time.Duration{-2 * time.Second, -200 * time.Millisecond, 200 * time.Millisecond} {
		t.Run(offset.String(), func(t *testing.T) {
			cs, vss := pacedState(t, commit.Add(offset))
			cs.pacingStarted = false

			pacingCommitHeight(t, cs, vss, 0, commit)

			require.Equal(t, cs.state.Timeouts.TimeoutCommit, cs.rs.StartTime.Sub(commit),
				"startup cadence must anchor on a common observed event, not each validator's startup time")
		})
	}
}

func TestPacingRejoinAfterRestartStartsOnGrid(t *testing.T) {
	commit := time.Now().Add(time.Hour)
	// A node on the grid started 870 ms before the commit, so it starts again at commit + 130 ms.
	grid, gridVals := pacedState(t, commit.Add(-870*time.Millisecond))
	pacingCommitHeight(t, grid, gridVals, 0, commit)

	// A restarted node has no paced anchor when it commits the same block.
	rejoin, rejoinVals := pacedState(t, commit.Add(-5*time.Second))
	rejoin.pacingStarted = false
	pacingCommitHeight(t, rejoin, rejoinVals, 0, commit)

	require.False(t, rejoin.rs.StartTime.After(grid.rs.StartTime),
		"a rejoining node must not start later than the nodes already on the grid")
	requirePacedNextHeight(t, rejoin, rejoinVals)
}

// requirePacedNextHeight commits the next height early and checks it is held by the floor.
func requirePacedNextHeight(t *testing.T, cs *State, vss []*validatorStub) {
	t.Helper()
	start := cs.rs.StartTime
	pacingCommitHeight(t, cs, vss, 0, start.Add(300*time.Millisecond))
	require.Equal(t, start.Add(cs.state.Timeouts.DynamicTimeoutCommit), cs.rs.StartTime,
		"the height after a bootstrap must pace from its start")
}

func TestPacingBackwardClockStepRestartsFromLegacyStart(t *testing.T) {
	for _, step := range []time.Duration{time.Second, 2 * time.Second} {
		t.Run(step.String(), func(t *testing.T) {
			commit := time.Now().Add(time.Hour)
			// The height started 700 ms before the commit, then the clock stepped back.
			cs, vss := pacedState(t, commit.Add(-700*time.Millisecond).Add(step))

			pacingCommitHeight(t, cs, vss, 0, commit)

			require.Equal(t, commit.Add(cs.state.Timeouts.TimeoutCommit), cs.rs.StartTime,
				"after a backward clock step the next height must start one TimeoutCommit after the commit")
			requirePacedNextHeight(t, cs, vss)
		})
	}
}

func TestPacingPrecommitQuorumStartsRoundZeroBeforeDeadline(t *testing.T) {
	cs, vss := pacedState(t, time.Now())
	cs.SetPrivValidator(nil)
	cs.decideProposal = func(int64, int32) {}
	cs.rs.StartTime = time.Now().Add(time.Hour)
	pacingAddPrecommitQuorum(t, cs, vss, 0)

	cs.enterNewRound(cs.rs.Height, 0)

	require.Equal(t, cstypes.RoundStepPropose, cs.rs.Step,
		"a committing quorum for the height must not be held by the floor")
}

func TestPacingMultiRoundHeightStartsNextWithinFloorOfCommit(t *testing.T) {
	for name, tc := range map[string]struct{ start, commit time.Duration }{
		"rounds overran the floor": {start: -3 * time.Second, commit: -100 * time.Millisecond},
		"rounds within the floor":  {start: -300 * time.Millisecond, commit: -50 * time.Millisecond},
	} {
		t.Run(name, func(t *testing.T) {
			now := time.Now()
			cs, vss := pacedState(t, now.Add(tc.start))
			commit := now.Add(tc.commit)

			pacingCommitHeight(t, cs, vss, 1, commit)

			require.False(t, cs.rs.StartTime.Before(commit), "the next height must not start before the commit")
			require.False(t, cs.rs.StartTime.After(commit.Add(testDynamicTimeoutCommit)),
				"a multi-round height must not delay the next one by more than DynamicTimeoutCommit")
		})
	}
}

func TestPacingForwardClockJumpRestartsCadence(t *testing.T) {
	// The wall clock jumped an hour forward after the height started.
	cs, vss := pacedState(t, time.Now().Add(-time.Hour))
	before := time.Now()

	pacingCommitHeight(t, cs, vss, 0, cs.rs.StartTime.Add(100*time.Millisecond))

	require.False(t, cs.rs.StartTime.Before(before), "a stale deadline must not be carried past the jump")
	require.False(t, cs.rs.StartTime.After(time.Now()), "the jump must not add a wait")
	jumped := cs.rs.StartTime

	pacingCommitHeight(t, cs, vss, 0, jumped.Add(100*time.Millisecond))

	require.Equal(t, jumped.Add(testDynamicTimeoutCommit), cs.rs.StartTime,
		"the cadence must resume from the post-jump start without a catch-up burst")
}

func TestPacingAnyPrecommitQuorumStartsRoundZeroBeforeDeadline(t *testing.T) {
	cs, vss := pacedState(t, time.Now())
	cs.SetPrivValidator(nil)
	cs.decideProposal = func(int64, int32) {}
	recorder := &recordingPropagator{NoOpPropagator: propagation.NewNoOpPropagator()}
	cs.propagator = recorder
	cs.rs.StartTime = time.Now().Add(time.Hour)
	ext := cs.state.ConsensusParams.ABCI.VoteExtensionsEnabled(cs.rs.Height)
	header := types.PartSetHeader{Total: 1, Hash: cmtrand.Bytes(tmhash.Size)}
	for _, vs := range vss[1:] {
		vs.Height, vs.Round = cs.rs.Height, 0
	}
	// +2/3 precommits without a majority: two for a block, one for nil.
	votes := signVotes(cmtproto.PrecommitType, cmtrand.Bytes(tmhash.Size), header, ext, vss[1:3]...)
	votes = append(votes, signVotes(cmtproto.PrecommitType, nil, types.PartSetHeader{}, ext, vss[3])...)
	for _, vote := range votes {
		_, err := cs.addVote(vote, "peer")
		require.NoError(t, err)
	}

	calls := recorder.snapshotCalls()
	require.NotEmpty(t, calls, "round 0 must start before moving to the precommit wait")
	require.Equal(t, proposerAndRoundCall{height: cs.rs.Height, round: 0, proposer: calls[0].proposer}, calls[0])
	require.Equal(t, cstypes.RoundStepPrecommitWait, cs.rs.Step)
}

func TestPacingDisabledKeepsLegacyWaitForTxs(t *testing.T) {
	cs, _ := randState(4)
	t.Cleanup(func() { require.NoError(t, cs.eventBus.Stop()) })
	prevoted := pacingCompleteProposal(t, cs)
	pacingWaitForTxs(t, cs)
	cs.rs.StartTime = time.Now().Add(-time.Second)

	cs.enterNewRound(cs.rs.Height, 0)

	require.False(t, *prevoted, "as on main, round 0 waits for txs when DynamicTimeoutCommit = 0")
	require.Equal(t, cstypes.RoundStepNewRound, cs.rs.Step)
}

func TestPacingProposalCompletedWhileWaitingForTxsIsPrevoted(t *testing.T) {
	cs, _ := pacedState(t, time.Now())
	cs.config.CreateEmptyBlocks = false
	prevoted := pacingCompleteProposal(t, cs)
	cs.rs.Step = cstypes.RoundStepNewRound

	cs.handleCompleteProposal(cs.rs.Height)

	require.True(t, *prevoted, "only NewHeight holds a proposal; NewRound (waiting for txs) must not")
}

func TestPacingUpgradeHeightBootstrapsFromLegacyStart(t *testing.T) {
	cs, vss := pacingRandState(t)
	cs.state.Timeouts.TimeoutCommit = 500 * time.Millisecond
	commit := time.Now().Add(time.Hour)
	cs.rs.StartTime = commit.Add(-2 * time.Second)
	cs.rs.CommitTime = commit
	cs.rs.CommitRound = 0
	pacingAddPrecommitQuorum(t, cs, vss, 0)
	// The upgrade block's FinalizeBlock returns the new values.
	nextState := pacingNextState(cs)
	nextState.Timeouts.TimeoutCommit = 100 * time.Millisecond
	nextState.Timeouts.DynamicTimeoutCommit = 800 * time.Millisecond

	cs.updateToState(nextState)

	// No dynamic height preceded it, so the upgrade height keeps the legacy start
	// (TimeoutCommit lags one height, as on main) and the next height paces from it.
	require.Equal(t, commit.Add(500*time.Millisecond), cs.rs.StartTime)
	requirePacedNextHeight(t, cs, vss)
}

// pacingNoProofStore reports an unchanged app hash, so no proof block is needed.
type pacingNoProofStore struct {
	sm.BlockStore
	appHash []byte
}

func (s pacingNoProofStore) LoadBlockMeta(int64) *types.BlockMeta {
	return &types.BlockMeta{Header: types.Header{AppHash: s.appHash}}
}

func pacingRandBlockID() types.BlockID {
	return types.BlockID{
		Hash:          cmtrand.Bytes(tmhash.Size),
		PartSetHeader: types.PartSetHeader{Total: 1, Hash: cmtrand.Bytes(tmhash.Size)},
	}
}

// pacingAddPrecommitQuorum adds +2/3 precommits for a random block at the current height.
func pacingAddPrecommitQuorum(t *testing.T, cs *State, vss []*validatorStub, round int32) {
	t.Helper()
	pacingAddPrecommits(t, cs, vss, round, pacingRandBlockID())
}

func pacingAddPrecommits(t *testing.T, cs *State, vss []*validatorStub, round int32, blockID types.BlockID) {
	t.Helper()
	ext := cs.state.ConsensusParams.ABCI.VoteExtensionsEnabled(cs.rs.Height)
	cs.rs.Votes.SetRound(round)
	for _, vs := range vss[1:] {
		vs.Height, vs.Round = cs.rs.Height, round
	}
	for _, vote := range signVotes(cmtproto.PrecommitType, blockID.Hash, blockID.PartSetHeader, ext, vss[1:]...) {
		added, err := cs.rs.Votes.AddVote(vote, "peer", ext)
		require.NoError(t, err)
		require.True(t, added)
	}
}

// pacingNextState is the state finalizeCommit passes to updateToState.
func pacingNextState(cs *State) sm.State {
	nextState := cs.state.Copy()
	nextState.LastBlockHeight = cs.rs.Height
	nextState.LastValidators = cs.rs.Validators
	return nextState
}

// pacingCommitHeight commits a block of the current height at round and commit time,
// then moves to the next height as finalizeCommit does.
func pacingCommitHeight(t *testing.T, cs *State, vss []*validatorStub, round int32, commit time.Time) {
	t.Helper()
	pacingCommitBlock(t, cs, vss, round, commit, pacingRandBlockID())
}

func pacingCommitBlock(t *testing.T, cs *State, vss []*validatorStub, round int32, commit time.Time, blockID types.BlockID) {
	t.Helper()
	pacingAddPrecommits(t, cs, vss, round, blockID)
	cs.rs.CommitRound = round
	cs.rs.CommitTime = commit
	nextState := pacingNextState(cs)
	nextState.LastBlockID = blockID
	cs.updateToState(nextState)
}

// pacingProposal is a round 0 proposal for blockID at the current height, stamped ts.
func pacingProposal(cs *State, blockID types.BlockID, ts time.Time) *types.Proposal {
	return &types.Proposal{
		Type: cmtproto.ProposalType, Height: cs.rs.Height, POLRound: -1, BlockID: blockID, Timestamp: ts,
	}
}

// pacingCommitProposal commits, in round 0 at commit, the block of a proposal stamped propTs.
func pacingCommitProposal(t *testing.T, cs *State, vss []*validatorStub, commit, propTs time.Time) {
	t.Helper()
	blockID := pacingRandBlockID()
	cs.rs.Proposal = pacingProposal(cs, blockID, propTs)
	pacingCommitBlock(t, cs, vss, 0, commit, blockID)
}

// countingHistogram counts observations.
type countingHistogram struct{ n int }

func (h *countingHistogram) With(...string) metrics.Histogram { return h }
func (h *countingHistogram) Observe(float64)                  { h.n++ }

type pacingMetrics struct {
	bypass *labelCounter
	waits  *countingHistogram
}

// recordPacingMetrics replaces cs's pacing metrics with recorders.
func recordPacingMetrics(cs *State) *pacingMetrics {
	m := &pacingMetrics{
		bypass: &labelCounter{mtx: &cmtsync.Mutex{}, counts: map[string]float64{}},
		waits:  &countingHistogram{},
	}
	cs.metrics.PacingBypass, cs.metrics.PacingFloorWaitSeconds = m.bypass, m.waits
	return m
}

func (m *pacingMetrics) requireCounts(t *testing.T, proposal, slow, bootstrap float64, waits int) {
	t.Helper()
	require.Equal(t, proposal, m.bypass.get("reason", "proposal"), "pacing_bypass{proposal}")
	require.Equal(t, slow, m.bypass.get("reason", "slow"), "pacing_bypass{slow}")
	require.Equal(t, bootstrap, m.bypass.get("reason", "bootstrap"), "pacing_bypass{bootstrap}")
	require.Equal(t, waits, m.waits.n, "pacing_floor_wait_seconds observations")
}

func TestPacingSkipTimeoutCommitSkipsOnlyLegacyWait(t *testing.T) {
	for name, dynamic := range map[string]time.Duration{"legacy": 0, "dynamic": testDynamicTimeoutCommit} {
		t.Run(name, func(t *testing.T) {
			cs, vss := pacedState(t, time.Now())
			cs.state.Timeouts.DynamicTimeoutCommit = dynamic
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
			if dynamic == 0 {
				require.NotEqual(t, cstypes.RoundStepNewHeight, cs.rs.Step, "skip_timeout_commit must work as on main")
			} else {
				require.Equal(t, cstypes.RoundStepNewHeight, cs.rs.Step,
					"skip_timeout_commit must not bypass the DynamicTimeoutCommit deadline")
			}
		})
	}
}

func legacyStartTime(cfgTimeoutCommit, appTimeoutCommit time.Duration, commit time.Time) time.Time {
	if appTimeoutCommit == 0 {
		appTimeoutCommit = cfgTimeoutCommit
	}
	return commit.Add(appTimeoutCommit)
}

func TestPacingDisabledStartTimeMatchesLegacyRule(t *testing.T) {
	now := time.Now()
	for _, appTC := range []time.Duration{0, time.Nanosecond, 500 * time.Millisecond, 3 * time.Second} {
		for name, tc := range map[string]struct {
			prevStart, commit time.Duration
			pacingStarted     bool
		}{
			"fast height":                 {prevStart: -300 * time.Millisecond, commit: -50 * time.Millisecond, pacingStarted: true},
			"slow height":                 {prevStart: -10 * time.Second, commit: -5 * time.Second, pacingStarted: true},
			"commit before start":         {prevStart: 10 * time.Second, commit: 0},
			"after a dynamic height":      {prevStart: -time.Second, commit: -10 * time.Millisecond, pacingStarted: true},
			"commit in the future (skew)": {prevStart: time.Hour, commit: time.Hour + time.Second},
		} {
			t.Run(fmt.Sprintf("%v/%s", appTC, name), func(t *testing.T) {
				cs, vss := pacingRandState(t)
				require.Zero(t, cs.state.Timeouts.DynamicTimeoutCommit)
				cs.config.TimeoutCommit = 700 * time.Millisecond
				cs.state.Timeouts.TimeoutCommit = appTC
				cs.pacingStarted = tc.pacingStarted
				cs.rs.StartTime = now.Add(tc.prevStart)
				commit := now.Add(tc.commit)

				pacingCommitHeight(t, cs, vss, 0, commit)

				require.Equal(t, legacyStartTime(cs.config.TimeoutCommit, appTC, commit), cs.rs.StartTime,
					"DynamicTimeoutCommit = 0 must keep main's StartTime exactly")
				require.False(t, cs.pacingStarted)
			})
		}
	}
}

func TestPacingDisabledBootstrapMatchesLegacyRule(t *testing.T) {
	state, privVals := randGenesisState(4, false, 10, test.ConsensusParams())
	state.Timeouts.TimeoutCommit = 2 * time.Second
	before := time.Now()
	cs := newState(state, privVals[0], kvstore.NewInMemoryApplication())
	t.Cleanup(func() { require.NoError(t, cs.eventBus.Stop()) })
	after := time.Now()

	require.False(t, cs.rs.StartTime.Before(before.Add(2*time.Second)))
	require.False(t, cs.rs.StartTime.After(after.Add(2*time.Second)))
}

func TestPacingDisabledKeepsLegacyRoundZeroAndProposal(t *testing.T) {
	cs, _ := randState(4)
	t.Cleanup(func() { require.NoError(t, cs.eventBus.Stop()) })
	require.Zero(t, cs.state.Timeouts.DynamicTimeoutCommit)
	prevoted := pacingCompleteProposal(t, cs)
	cs.rs.StartTime = time.Now().Add(time.Hour)

	cs.handleCompleteProposal(cs.rs.Height)

	require.True(t, *prevoted, "as on main, a complete proposal during NewHeight is prevoted at once")

	cs2, _ := randState(4)
	t.Cleanup(func() { require.NoError(t, cs2.eventBus.Stop()) })
	cs2.SetPrivValidator(nil)
	cs2.rs.StartTime = time.Now().Add(time.Hour)

	cs2.enterNewRound(cs2.rs.Height, 0)

	require.Equal(t, cstypes.RoundStepPropose, cs2.rs.Step, "as on main, an early enterNewRound is not held")
}
