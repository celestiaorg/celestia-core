package time

import (
	"sort"
	"time"
)

// Now returns the current time in UTC with no monotonic component.
func Now() time.Time {
	return Canonical(time.Now())
}

// Canonical returns UTC time with no monotonic component.
// Stripping the monotonic component is for time equality.
// See https://github.com/tendermint/tendermint/pull/2203#discussion_r215064334
func Canonical(t time.Time) time.Time {
	return t.Round(0).UTC()
}

// WeightedTime for computing a median.
type WeightedTime struct {
	Time   time.Time
	Weight int64
}

// NewWeightedTime with time and weight.
func NewWeightedTime(time time.Time, weight int64) *WeightedTime {
	return &WeightedTime{
		Time:   time,
		Weight: weight,
	}
}

// WeightedMedian computes weighted median time for a given array of WeightedTime and the total voting power.
// It returns the first time whose cumulative weight is strictly greater than half of totalVotingPower,
// so a set of times holding at most half of the weight cannot select the median on its own.
func WeightedMedian(weightedTimes []*WeightedTime, totalVotingPower int64) time.Time {
	return weightedMedian(weightedTimes, totalVotingPower, true)
}

// LegacyWeightedMedian computes the weighted median with the rule used before
// the strict-majority fix: the first time whose cumulative weight reaches
// floor(totalVotingPower/2). It lets a participant with exactly that weight
// select the median, so use it only to make and validate blocks for app
// versions that committed under this rule.
func LegacyWeightedMedian(weightedTimes []*WeightedTime, totalVotingPower int64) time.Time {
	return weightedMedian(weightedTimes, totalVotingPower, false)
}

func weightedMedian(weightedTimes []*WeightedTime, totalVotingPower int64, strict bool) (res time.Time) {
	median := totalVotingPower / 2

	sort.Slice(weightedTimes, func(i, j int) bool {
		if weightedTimes[i] == nil {
			return false
		}
		if weightedTimes[j] == nil {
			return true
		}
		return weightedTimes[i].Time.Before(weightedTimes[j].Time)
	})

	for _, weightedTime := range weightedTimes {
		if weightedTime != nil {
			if median < weightedTime.Weight || (!strict && median == weightedTime.Weight) {
				res = weightedTime.Time
				break
			}
			median -= weightedTime.Weight
		}
	}
	return
}
