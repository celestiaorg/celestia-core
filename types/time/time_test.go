package time

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWeightedMedian(t *testing.T) {
	m := make([]*WeightedTime, 3)

	t1 := Now()
	t2 := t1.Add(5 * time.Second)
	t3 := t1.Add(10 * time.Second)

	m[2] = NewWeightedTime(t1, 33) // faulty processes
	m[0] = NewWeightedTime(t2, 40) // correct processes
	m[1] = NewWeightedTime(t3, 27) // correct processes
	totalVotingPower := int64(100)

	median := WeightedMedian(m, totalVotingPower)
	assert.Equal(t, t2, median)
	// median always returns value between values of correct processes
	assert.Equal(t, true, (median.After(t1) || median.Equal(t1)) &&
		(median.Before(t3) || median.Equal(t3)))

	m[1] = NewWeightedTime(t1, 40) // correct processes
	m[2] = NewWeightedTime(t2, 27) // correct processes
	m[0] = NewWeightedTime(t3, 33) // faulty processes
	totalVotingPower = int64(100)

	median = WeightedMedian(m, totalVotingPower)
	assert.Equal(t, t2, median)
	// median always returns value between values of correct processes
	assert.Equal(t, true, (median.After(t1) || median.Equal(t1)) &&
		(median.Before(t2) || median.Equal(t2)))

	m = make([]*WeightedTime, 8)
	t4 := t1.Add(15 * time.Second)
	t5 := t1.Add(60 * time.Second)

	m[3] = NewWeightedTime(t1, 10) // correct processes
	m[1] = NewWeightedTime(t2, 10) // correct processes
	m[5] = NewWeightedTime(t2, 10) // correct processes
	m[4] = NewWeightedTime(t3, 23) // faulty processes
	m[0] = NewWeightedTime(t4, 20) // correct processes
	m[7] = NewWeightedTime(t5, 10) // faulty processes
	totalVotingPower = int64(83)

	median = WeightedMedian(m, totalVotingPower)
	assert.Equal(t, t3, median)
	// median always returns value between values of correct processes
	assert.Equal(t, true, (median.After(t1) || median.Equal(t1)) &&
		(median.Before(t4) || median.Equal(t4)))
}

// TestWeightedMedianFarFutureTimes guards against the int64 nanosecond overflow
// bug: time.Time.UnixNano() is undefined for dates beyond ~2262-04-11, so
// comparing UnixNano() values sorts such times incorrectly and corrupts the
// median. WeightedMedian must order times with time.Time.Before, which is
// correct across the overflow boundary.
func TestWeightedMedianFarFutureTimes(t *testing.T) {
	// base sits just inside the representable UnixNano range.
	base := time.Date(2262, time.April, 11, 0, 0, 0, 0, time.UTC)
	t1 := base                           // before the overflow boundary
	t2 := base.Add(48 * time.Hour)       // past the boundary -> UnixNano wraps negative
	t3 := base.Add(365 * 24 * time.Hour) // well past the boundary

	// Sanity check: these timestamps actually straddle the UnixNano overflow,
	// so a UnixNano-based comparison would mis-order them.
	require.True(t, t2.UnixNano() < t1.UnixNano(),
		"expected UnixNano to overflow past the year-2262 boundary")
	require.True(t, t1.Before(t2) && t2.Before(t3),
		"chronological order must be t1 < t2 < t3")

	// totalVotingPower 100, weights 30/40/30 -> the weighted median is t2.
	m := []*WeightedTime{
		NewWeightedTime(t3, 30),
		NewWeightedTime(t1, 30),
		NewWeightedTime(t2, 40),
	}
	median := WeightedMedian(m, 100)
	require.Equal(t, t2, median,
		"WeightedMedian must order times correctly across the UnixNano overflow boundary")
}
