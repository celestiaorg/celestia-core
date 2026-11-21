package main

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	e2e "github.com/cometbft/cometbft/test/e2e/pkg"
	"github.com/cometbft/cometbft/types"
)

func TestLowestDuplicateVoteHeight(t *testing.T) {
	half := e2e.EvidenceAgeHeight / 2
	testCases := []struct {
		name            string
		initialHeight   int64
		evidenceHeight  int64
		earliestHeights []int64
		want            int64
	}{
		{"evidence age bound", 1, 100, []int64{1, 1}, 100 - half},
		{"initial height bound", 1, 3, []int64{1}, 1},
		{"initial height above one", 50, 52, nil, 50},
		{"late node bound", 1, 100, []int64{1, 97, 60}, 97},
		{"node at evidence height", 1, 100, []int64{100}, 100},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := lowestDuplicateVoteHeight(tc.initialHeight, tc.evidenceHeight, tc.earliestHeights)
			require.Equal(t, tc.want, got)
		})
	}
}

// targetsFixture returns n mock private validators and a validator set with
// those validators plus one validator whose key the runner does not have.
func targetsFixture(t *testing.T, n int) ([]types.MockPV, *types.ValidatorSet) {
	t.Helper()
	privVals := make([]types.MockPV, n)
	vals := make([]*types.Validator, 0, n+1)
	for i := range privVals {
		privVals[i] = types.NewMockPV()
		vals = append(vals, types.NewValidator(privVals[i].PrivKey.PubKey(), 10))
	}
	unknown := types.NewMockPV()
	vals = append(vals, types.NewValidator(unknown.PrivKey.PubKey(), 10))
	return privVals, types.NewValidatorSet(vals)
}

type pair struct {
	height  int64
	address string
}

func TestDuplicateVoteTargets(t *testing.T) {
	const chainID = "test-chain"
	evTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	privVals, valSet := targetsFixture(t, 3)

	var fetched []int64
	targets := &duplicateVoteTargets{
		privVals:  privVals,
		minHeight: 8,
		height:    11,
		fetch: func(height int64) (time.Time, *types.ValidatorSet, error) {
			fetched = append(fetched, height)
			return evTime, valSet, nil
		},
	}

	seen := map[pair]bool{}
	for i := 0; i < 9; i++ {
		ev, err := targets.generate(chainID)
		require.NoError(t, err)
		require.Equal(t, ev.VoteA.Height, ev.VoteB.Height)
		require.NoError(t, ev.ValidateBasic())
		p := pair{ev.Height(), ev.VoteA.ValidatorAddress.String()}
		require.False(t, seen[p], "duplicate (height, validator) pair %v", p)
		seen[p] = true
		require.Equal(t, int64(10-i/3), ev.Height(), "evidence %d", i)
		_, val := valSet.GetByAddress(ev.VoteA.ValidatorAddress)
		require.NotNil(t, val)
		require.Equal(t, valSet.TotalVotingPower(), ev.TotalVotingPower)
	}
	require.Equal(t, []int64{10, 9, 8}, fetched)

	// Every pair at or above the minimum height is used, and the validator
	// without a key is skipped. The walk must not go below the minimum.
	for i := 0; i < 2; i++ {
		_, err := targets.generate(chainID)
		require.ErrorIs(t, err, errNoDuplicateVoteTarget)
	}
	require.Equal(t, []int64{10, 9, 8}, fetched)
}

func TestDuplicateVoteTargetsMinHeightIsUsed(t *testing.T) {
	privVals, valSet := targetsFixture(t, 1)
	targets := &duplicateVoteTargets{
		privVals:  privVals,
		minHeight: 5,
		height:    6,
		fetch: func(int64) (time.Time, *types.ValidatorSet, error) {
			return time.Now(), valSet, nil
		},
	}
	ev, err := targets.generate("test-chain")
	require.NoError(t, err)
	require.Equal(t, int64(5), ev.Height())
	_, err = targets.generate("test-chain")
	require.ErrorIs(t, err, errNoDuplicateVoteTarget)
}

func TestDuplicateVoteTargetsFetchError(t *testing.T) {
	privVals, _ := targetsFixture(t, 1)
	fetchErr := errors.New("height 9 is not available, lowest height is 10")
	targets := &duplicateVoteTargets{
		privVals:  privVals,
		minHeight: 1,
		height:    10,
		fetch: func(int64) (time.Time, *types.ValidatorSet, error) {
			return time.Time{}, nil, fetchErr
		},
	}
	_, err := targets.generate("test-chain")
	require.ErrorIs(t, err, fetchErr)
}
