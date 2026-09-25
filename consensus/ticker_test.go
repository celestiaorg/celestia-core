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
	cs, _ := randState(4)
	t.Cleanup(func() { require.NoError(t, cs.eventBus.Stop()) })
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

func TestTimeoutTickerHeightOnlyRespectsEachHeight(t *testing.T) {
	ticker := newHeightOnlyTicker()
	require.NoError(t, ticker.Start())
	t.Cleanup(func() { require.NoError(t, ticker.Stop()) })
	for height := int64(1); height <= 2; height++ {
		const delay = 20 * time.Millisecond
		start := time.Now()
		ticker.ScheduleTimeout(timeoutInfo{Duration: delay, Height: height, Step: types.RoundStepNewHeight})
		// Round deadlines must not interrupt the height pacing deadline.
		ticker.ScheduleTimeout(timeoutInfo{Height: height, Step: types.RoundStepPropose})
		select {
		case timeout := <-ticker.Chan():
			require.Equal(t, height, timeout.Height)
			require.Equal(t, types.RoundStepNewHeight, timeout.Step)
			require.GreaterOrEqual(t, time.Since(start), delay, "the test ticker must honor the height deadline")
		case <-time.After(time.Second):
			t.Fatal("the test ticker must fire at every height")
		}
	}
}
