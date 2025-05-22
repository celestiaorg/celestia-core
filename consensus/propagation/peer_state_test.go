package propagation

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/p2p/mock"

	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/libs/bits"
)

// newTestPeerState is a helper to create a PeerState with a mock peer.
func newTestPeerState() *PeerState {
	peer := mock.Peer{}
	return newPeerState(&peer, log.NewNopLogger())
}

func TestPeerState_SetRequests(t *testing.T) {
	tests := []struct {
		name        string
		height      int64
		round       int32
		requestBits *bits.BitArray
	}{
		{
			name:        "small request set",
			height:      10,
			round:       1,
			requestBits: bits.NewBitArray(3),
		},
		{
			name:        "empty bit array",
			height:      5,
			round:       0,
			requestBits: bits.NewBitArray(0),
		},
		{
			name:        "larger request set",
			height:      100,
			round:       2,
			requestBits: bits.NewBitArray(10),
		},
	}

	for _, tt := range tests {
		tt := tt // pin
		t.Run(tt.name, func(t *testing.T) {
			ps := newTestPeerState()
			ps.AddRequests(tt.height, tt.round, tt.requestBits)

			gotRequests, ok := ps.GetRequests(tt.height, tt.round)
			if tt.requestBits.Size() == 0 {
				// If requestBits had 0 size, we expect not to store anything.
				require.False(t, ok, "GetRequests should be false if requests is 0 size")
				return
			}
			require.True(t, ok)
			require.Equal(t, tt.requestBits.Size(), gotRequests.Size())
		})
	}
}

func TestPeerState_DeleteHeight(t *testing.T) {
	ps := newTestPeerState()
	heightToDelete := int64(10)
	bm := bits.NewBitArray(10)
	bm.Fill()
	// Create some data at height=10, round=1
	ps.AddHaves(heightToDelete, 1, bm)
	// Also create data at a different height
	ps.AddHaves(20, 0, bm)

	// Now delete the data for height=10
	ps.DeleteHeight(heightToDelete)

	// Verify height=10 data is gone
	_, ok := ps.GetHaves(heightToDelete, 1)
	require.False(t, ok, "Expected no data for height=10 after DeleteHeight")

	// But height=20 data remains
	_, ok2 := ps.GetHaves(20, 0)
	require.True(t, ok2, "Should still have data for other heights")
}

func TestPeerState_prune(t *testing.T) {
	ps := newTestPeerState()

	/*
	   We'll create data for heights = [10, 11, 12, 13], with multiple rounds each.
	   Then we'll prune with:
	     currentHeight=13
	     keepRecentHeights=2
	     keepRecentRounds=1
	   Expected:
	     - Any height < 13 - 2 = 11 should be removed entirely. (height 10 removed)
	     - For the non-current height 11 & 12, delete all rounds < 13 - 1 = 12.
	       So for height=11, all rounds < 12 are removed => that means rounds 0..11 removed
	       For height=12, all rounds < 12 are removed => that means rounds 0..11 removed
	     - For currentHeight=13, do nothing except keep all rounds.
	*/

	bm := bits.NewBitArray(10)
	bm.Fill()

	// Populate test data
	height := int64(13)
	for h := int64(10); h <= 13; h++ {
		for r := int32(0); r < 3; r++ {
			ps.AddHaves(h, r, bm)
		}
	}

	// Prune
	ps.prune(height - 2)

	// Height < 11 => remove
	_, okH10r0 := ps.GetHaves(10, 0)
	require.False(t, okH10r0, "height=10 should be removed entirely")

	// Height=11 => keep only round >= 12 => that means no rounds at all (0..2 < 12 => all removed)
	_, okH11r0 := ps.GetHaves(11, 0)
	require.True(t, okH11r0, "all rounds at height=11 removed")

	_, okH11r2 := ps.GetHaves(11, 2)
	require.True(t, okH11r2, "all rounds at height=11 removed")

	// Height=12 => keep only round >= 12 => that means round=0..2 <12 => removed
	_, okH12r2 := ps.GetHaves(12, 2)
	require.True(t, okH12r2, "all rounds at height=12 removed")

	// Height=13 => do nothing, keep all rounds
	for r := int32(0); r < 3; r++ {
		_, ok := ps.GetHaves(13, r)
		require.True(t, ok, "height=13 round=%d should remain", r)
	}
}

func TestPeerState_IncreaseConcurrentReqs(t *testing.T) {
	tests := []struct {
		name      string
		initial   int64
		increment int64
		expected  int64
	}{
		{
			name:      "increment by positive value",
			initial:   0,
			increment: 10,
			expected:  10,
		},
		{
			name:      "increment by zero",
			initial:   5,
			increment: 0,
			expected:  5,
		},
		{
			name:      "increment by negative value",
			initial:   10,
			increment: -5,
			expected:  5,
		},
	}

	for _, tt := range tests {
		tt := tt // pin
		t.Run(tt.name, func(t *testing.T) {
			ps := newTestPeerState()
			ps.SetConcurrentReqs(tt.initial)

			ps.IncreaseConcurrentReqs(tt.increment)

			assert.Equal(t, tt.expected, ps.concurrentReqs.Load())
		})
	}
}

func TestPeerState_SetConcurrentReqs(t *testing.T) {
	tests := []struct {
		name     string
		count    int64
		expected int64
	}{
		{
			name:     "set positive count",
			count:    5,
			expected: 5,
		},
		{
			name:     "set zero count",
			count:    0,
			expected: 0,
		},
		{
			name:     "set negative count",
			count:    -3,
			expected: -3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ps := newPeerState(nil, nil)
			ps.SetConcurrentReqs(tt.count)

			got := ps.concurrentReqs.Load()
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestPeerState_DecreaseConcurrentReqs(t *testing.T) {
	tests := []struct {
		name          string
		initialCount  int64
		decrease      int64
		expectedCount int64
	}{
		{
			name:          "decrease from positive count",
			initialCount:  10,
			decrease:      3,
			expectedCount: 7,
		},
		{
			name:          "decrease to zero",
			initialCount:  5,
			decrease:      5,
			expectedCount: 0,
		},
		{
			name:          "decrease more than count",
			initialCount:  5,
			decrease:      10,
			expectedCount: 0,
		},
		{
			name:          "decrease from zero",
			initialCount:  0,
			decrease:      5,
			expectedCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ps := newPeerState(nil, nil)
			ps.SetConcurrentReqs(tt.initialCount)

			ps.DecreaseConcurrentReqs(tt.decrease)

			got := ps.concurrentReqs.Load()
			assert.Equal(t, tt.expectedCount, got)
		})
	}
}
