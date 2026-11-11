package consensus

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/abci/example/kvstore"
	"github.com/cometbft/cometbft/crypto/tmhash"
	"github.com/cometbft/cometbft/internal/test"
	cmtrand "github.com/cometbft/cometbft/libs/rand"
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

func TestPacingUsesLocalStartAcrossValidatorClockOffsets(t *testing.T) {
	base := time.Now().Add(time.Hour)
	for _, offset := range []time.Duration{-2 * time.Second, 0, 2 * time.Second} {
		t.Run(offset.String(), func(t *testing.T) {
			localStart := base.Add(offset)
			cs, vss := pacedState(t, localStart)
			// Every validator sees the same proposer timestamp; it must not change the interval.
			cs.rs.Proposal = &types.Proposal{Timestamp: base.Add(300 * time.Millisecond)}

			pacingCommitHeight(t, cs, vss, 0, localStart.Add(400*time.Millisecond))

			require.Equal(t, testDynamicTimeoutCommit, cs.rs.StartTime.Sub(localStart),
				"proposal construction and a remote validator's clock must not alter the height interval")
		})
	}
}

func TestPacingSlowFinalizationDoesNotLeaveStaleStart(t *testing.T) {
	start := time.Now().Add(-5 * time.Second)
	cs, vss := pacedState(t, start)
	finalizationComplete := time.Now()

	pacingCommitHeight(t, cs, vss, 0, start.Add(100*time.Millisecond))

	require.False(t, cs.rs.StartTime.Before(finalizationComplete),
		"a long FinalizeBlock must not leave a stale anchor that lets the following height run early")
}

func TestPacingCommitBeforeScheduledStartDoesNotAccumulateDelay(t *testing.T) {
	now := time.Now()
	cs, vss := pacedState(t, now.Add(10*time.Second))

	pacingCommitHeight(t, cs, vss, 0, now)

	require.False(t, cs.rs.StartTime.After(time.Now().Add(time.Second)),
		"committing before a scheduled start must not carry an unused deadline into later heights")
}

func TestPacingFirstCommitAlignsLocalStartupTimes(t *testing.T) {
	commit := time.Now().Add(time.Hour)
	for _, offset := range []time.Duration{-2 * time.Second, -200 * time.Millisecond, 200 * time.Millisecond} {
		t.Run(offset.String(), func(t *testing.T) {
			cs, vss := pacedState(t, commit.Add(offset))
			cs.pacingStarted = false

			pacingCommitHeight(t, cs, vss, 0, commit)

			require.Equal(t, testDynamicTimeoutCommit, cs.rs.StartTime.Sub(commit),
				"startup cadence must anchor on a common observed event, not each validator's startup time")
		})
	}
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

func TestPacingUpgradeHeightUsesNewDynamicAtOnce(t *testing.T) {
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

	// TimeoutCommit lags one height (as on main); the dynamic floor applies at once,
	// anchored on the commit since no dynamic height preceded it.
	require.Equal(t, commit.Add(800*time.Millisecond), cs.rs.StartTime)
}

// pacingAddPrecommitQuorum adds +2/3 precommits for the current height.
func pacingAddPrecommitQuorum(t *testing.T, cs *State, vss []*validatorStub, round int32) {
	t.Helper()
	ext := cs.state.ConsensusParams.ABCI.VoteExtensionsEnabled(cs.rs.Height)
	cs.rs.Votes.SetRound(round)
	header := types.PartSetHeader{Total: 1, Hash: cmtrand.Bytes(tmhash.Size)}
	for _, vs := range vss[1:] {
		vs.Height, vs.Round = cs.rs.Height, round
	}
	for _, vote := range signVotes(cmtproto.PrecommitType, cmtrand.Bytes(tmhash.Size), header, ext, vss[1:]...) {
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

// pacingCommitHeight commits the current height at round and commit time,
// then moves to the next height as finalizeCommit does.
func pacingCommitHeight(t *testing.T, cs *State, vss []*validatorStub, round int32, commit time.Time) {
	t.Helper()
	pacingAddPrecommitQuorum(t, cs, vss, round)
	cs.rs.CommitRound = round
	cs.rs.CommitTime = commit
	cs.updateToState(pacingNextState(cs))
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
