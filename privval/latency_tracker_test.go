package privval

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/libs/log"
)

type capturingLogger struct {
	mtx      sync.Mutex
	infoMsgs []string
}

func (l *capturingLogger) Trace(string, ...interface{}) {}
func (l *capturingLogger) Debug(string, ...interface{}) {}
func (l *capturingLogger) Error(string, ...interface{}) {}
func (l *capturingLogger) Info(msg string, _ ...interface{}) {
	l.mtx.Lock()
	defer l.mtx.Unlock()
	l.infoMsgs = append(l.infoMsgs, msg)
}
func (l *capturingLogger) With(...interface{}) log.Logger { return l }

func (l *capturingLogger) infoCount() int {
	l.mtx.Lock()
	defer l.mtx.Unlock()
	return len(l.infoMsgs)
}

func TestSigningLatencyTrackerMedian(t *testing.T) {
	tracker := newSigningLatencyTracker(NopMetrics(), log.NewNopLogger())

	// Fill with 1..50 ms. Median of 1..50 is 25.5ms.
	for i := 1; i <= signingLatencyWarnWindow; i++ {
		tracker.Record(time.Duration(i) * time.Millisecond)
	}

	tracker.mtx.Lock()
	median := tracker.medianLocked()
	tracker.mtx.Unlock()

	require.Equal(t, 25500*time.Microsecond, median)
}

func TestSigningLatencyTrackerWarnsWhenMedianAboveThreshold(t *testing.T) {
	logger := &capturingLogger{}
	tracker := newSigningLatencyTracker(NopMetrics(), logger)
	tracker.cooldown = time.Hour // avoid flakiness from real time

	for i := 0; i < signingLatencyWarnWindow; i++ {
		tracker.Record(20 * time.Millisecond)
	}
	require.Equal(t, 1, logger.infoCount())

	// Additional samples within cooldown should not warn again.
	for i := 0; i < signingLatencyWarnWindow; i++ {
		tracker.Record(20 * time.Millisecond)
	}
	require.Equal(t, 1, logger.infoCount())
}

func TestSigningLatencyTrackerNoWarnWhenMedianBelowThreshold(t *testing.T) {
	logger := &capturingLogger{}
	tracker := newSigningLatencyTracker(NopMetrics(), logger)

	for i := 0; i < signingLatencyWarnWindow; i++ {
		tracker.Record(5 * time.Millisecond)
	}
	require.Equal(t, 0, logger.infoCount())
}

func TestSigningLatencyTrackerNoWarnUntilWindowFull(t *testing.T) {
	logger := &capturingLogger{}
	tracker := newSigningLatencyTracker(NopMetrics(), logger)

	for i := 0; i < signingLatencyWarnWindow-1; i++ {
		tracker.Record(50 * time.Millisecond)
	}
	require.Equal(t, 0, logger.infoCount())

	tracker.Record(50 * time.Millisecond)
	require.Equal(t, 1, logger.infoCount())
}

func TestSigningLatencyTrackerCooldownAllowsLaterWarn(t *testing.T) {
	logger := &capturingLogger{}
	tracker := newSigningLatencyTracker(NopMetrics(), logger)

	for i := 0; i < signingLatencyWarnWindow; i++ {
		tracker.Record(20 * time.Millisecond)
	}
	require.Equal(t, 1, logger.infoCount())

	// Window is already full; further samples still evaluate the median, but
	// the cooldown gate must suppress another warn.
	tracker.Record(20 * time.Millisecond)
	require.Equal(t, 1, logger.infoCount())

	// Simulate wall-clock advancing past cooldown. Sleeping with a tiny
	// cooldown is flaky under -race: a second Record loop can outlast the
	// cooldown and emit more than one warn. Backdating lastWarn exercises the
	// same condition the production gate uses (now.Sub(lastWarn) < cooldown).
	tracker.mtx.Lock()
	tracker.lastWarn = time.Now().Add(-tracker.cooldown - time.Nanosecond)
	tracker.mtx.Unlock()

	tracker.Record(20 * time.Millisecond)
	require.Equal(t, 2, logger.infoCount())
}
