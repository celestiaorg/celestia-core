package consensus

import (
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLegacyPacingUsesConfigTimeouts(t *testing.T) {
	cs, vss := pacedState(t, time.Now())
	cs.state.Timeouts.DelayedPrecommitTimeout = 0
	cs.config.LegacyPacing = true
	cs.config.LegacyPacingTimeoutCommit = 200 * time.Millisecond
	cs.config.LegacyPacingDelayedPrecommitTimeout = 550 * time.Millisecond

	commit := time.Now()
	pacingCommitHeight(t, cs, vss, 0, commit)

	require.Equal(t, commit.Add(200*time.Millisecond), cs.rs.StartTime, "legacy rule: CommitTime + legacy timeout commit")
	require.Zero(t, cs.dynamicTimeoutCommit())
	require.Equal(t, testDynamicTimeoutCommit, cs.state.Timeouts.DynamicTimeoutCommit, "persisted app timeouts must not change")
	delay := cs.precommitDelay()
	require.Greater(t, delay, 500*time.Millisecond)
	require.LessOrEqual(t, delay, 750*time.Millisecond)

	cs.config.LegacyPacing = false
	require.Equal(t, testDynamicTimeoutCommit, cs.dynamicTimeoutCommit())
	require.Zero(t, cs.precommitDelay(), "without the switch the app's delayed precommit timeout (0) applies")
}

func TestLegacyPacingZeroTimeoutCommitMeansNoWait(t *testing.T) {
	cs, vss := pacedState(t, time.Now())
	cs.config.TimeoutCommit = time.Second
	cs.config.LegacyPacing = true
	cs.config.LegacyPacingTimeoutCommit = 0

	commit := time.Now()
	pacingCommitHeight(t, cs, vss, 0, commit)

	require.Equal(t, commit, cs.rs.StartTime, "an explicit legacy 0 must not fall back to config timeout_commit")
}

// medianInterval starts nodes with legacy pacing on the given ones and returns
// the median commit interval seen by the last node (never legacy) over heights 3 to target.
func medianInterval(t *testing.T, legacy []bool, target int64) time.Duration {
	t.Helper()
	css := newPacedNet(t, len(legacy), func() *pacedApp {
		return &pacedApp{Application: newKVStore(), dynamic: 300 * time.Millisecond, timeoutPropose: 5 * time.Second}
	})
	for i, on := range legacy {
		css[i].config.LegacyPacing = on
		css[i].config.LegacyPacingTimeoutCommit = 100 * time.Millisecond
		css[i].config.LegacyPacingDelayedPrecommitTimeout = 1500 * time.Millisecond
	}
	_, subs := startPacedNet(t, css, slices.Repeat([]bool{true}, len(legacy)))
	seen := waitHeight(t, subs[len(subs)-1], target)
	intervals := make([]time.Duration, 0, target)
	for h := int64(3); h <= target; h++ {
		intervals = append(intervals, seen[h].Sub(seen[h-1]))
	}
	slices.Sort(intervals)
	t.Logf("legacy %v: intervals %v", legacy, intervals)
	return intervals[len(intervals)/2]
}

func TestLegacyPacingSlowsNetworkAboveOneThirdOfStake(t *testing.T) {
	t.Run("half the stake", func(t *testing.T) {
		require.Greater(t, medianInterval(t, []bool{true, true, false, false}, 6), 1200*time.Millisecond,
			"with more than 1/3 of the stake on legacy pacing every height waits for its delayed precommits")
	})
	t.Run("a quarter of the stake", func(t *testing.T) {
		require.Less(t, medianInterval(t, []bool{true, false, false, false}, 8), time.Second,
			"below 1/3 the other validators commit without the delayed precommits")
		// The legacy node itself lags: main's delayed precommit sleeps on the receive routine.
	})
}
