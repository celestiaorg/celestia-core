package consensus

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/consensus/types"
)

func TestTimeoutTicker(t *testing.T) {
	ticker := NewTimeoutTicker()
	err := ticker.Start()
	require.NoError(t, err)
	defer func() {
		err := ticker.Stop()
		require.NoError(t, err)
	}()

	c := ticker.Chan()
	for i := 1; i <= 10; i++ {
		height := int64(i)

		startTime := time.Now()
		// Schedule a timeout for 5ms from now
		negTimeout := timeoutInfo{Duration: -1 * time.Millisecond, Height: height, Round: 0, Step: types.RoundStepNewHeight}
		timeout := timeoutInfo{Duration: 5 * time.Millisecond, Height: height, Round: 0, Step: types.RoundStepNewRound}
		ticker.ScheduleTimeout(negTimeout)
		ticker.ScheduleTimeout(timeout)

		// Wait for the timeout to be received
		to := <-c
		endTime := time.Now()
		elapsedTime := endTime.Sub(startTime)
		if timeout == to {
			require.True(t, elapsedTime >= timeout.Duration, "We got the 5ms timeout. However the timeout happened too quickly. Should be >= 5ms. Got %dms (start time %d end time %d)", elapsedTime.Milliseconds(), startTime.UnixMilli(), endTime.UnixMilli())
		}
	}
}

func TestTimeoutTickerRearmsNewHeightAfterEarlyFire(t *testing.T) {
	cs, _ := pacedState(t)
	cs.SetPrivValidator(nil)
	cs.decideProposal = func(int64, int32) {}
	cs.timeoutTicker = NewTimeoutTicker()
	require.NoError(t, cs.timeoutTicker.Start())
	t.Cleanup(func() { require.NoError(t, cs.timeoutTicker.Stop()) })

	// A timer measures elapsed time while StartTime uses the consensus wall
	// clock. Model a backward clock adjustment by firing before StartTime.
	cs.scheduleTimeout(0, cs.rs.Height, 0, types.RoundStepNewHeight)
	select {
	case timeout := <-cs.timeoutTicker.Chan():
		cs.rs.StartTime = time.Now().Add(50 * time.Millisecond)
		cs.handleTimeout(timeout, *cs.GetRoundState())
	case <-time.After(time.Second):
		t.Fatal("the initial NewHeight timeout did not fire")
	}
	require.Equal(t, types.RoundStepNewHeight, cs.rs.Step)

	select {
	case timeout := <-cs.timeoutTicker.Chan():
		require.False(t, time.Now().Before(cs.rs.StartTime))
		cs.handleTimeout(timeout, *cs.GetRoundState())
	case <-time.After(time.Second):
		t.Fatal("an early NewHeight timeout must be rearmed until StartTime")
	}
	require.Equal(t, types.RoundStepPropose, cs.rs.Step)
}

func TestTimeoutTickerRearmOnlyAfterFireAndOnlyWhenFlagged(t *testing.T) {
	ticker := NewTimeoutTicker()
	require.NoError(t, ticker.Start())
	t.Cleanup(func() { require.NoError(t, ticker.Stop()) })
	newHeight := timeoutInfo{Height: 1, Step: types.RoundStepNewHeight}
	expect := func(fire bool, within time.Duration) {
		t.Helper()
		select {
		case <-ticker.Chan():
			require.True(t, fire, "unexpected tock")
		case <-time.After(within):
			require.False(t, fire, "expected a tock")
		}
	}

	ticker.ScheduleTimeout(newHeight)
	expect(true, time.Second)
	// As on main, an unflagged duplicate of a fired step is dropped.
	ticker.ScheduleTimeout(newHeight)
	expect(false, 100*time.Millisecond)

	rearm := newHeight
	rearm.rearm = true
	ticker.ScheduleTimeout(rearm)
	expect(true, time.Second)

	// A re-arm must not replace an active timer.
	const active = 300 * time.Millisecond
	start := time.Now()
	ticker.ScheduleTimeout(timeoutInfo{Duration: active, Height: 2, Step: types.RoundStepNewHeight})
	ticker.ScheduleTimeout(timeoutInfo{Height: 2, Step: types.RoundStepNewHeight, rearm: true})
	expect(true, time.Second)
	require.GreaterOrEqual(t, time.Since(start), active)
}
