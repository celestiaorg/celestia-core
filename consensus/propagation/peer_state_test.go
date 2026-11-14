package propagation

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	proptypes "github.com/cometbft/cometbft/consensus/propagation/types"
	"github.com/cometbft/cometbft/libs/bits"
	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/p2p/mock"
	"github.com/cometbft/cometbft/types"
)

// newTestPeerState is a helper to create a PeerState with a mock peer.
func newTestPeerState() *PeerState {
	peer := mock.Peer{}
	return newPeerState(context.Background(), &peer, log.NewNopLogger())
}

func TestPeerState_MaxUnverifiedProposalHeight(t *testing.T) {
	ps := newTestPeerState()

	// no entries -> 0
	require.Equal(t, int64(0), ps.MaxUnverifiedProposalHeight())

	// add some heights out of order
	ps.unverifiedProposals[15] = &proptypes.CompactBlock{}
	ps.unverifiedProposals[10] = &proptypes.CompactBlock{}
	ps.unverifiedProposals[25] = &proptypes.CompactBlock{}

	require.Equal(t, int64(25), ps.MaxUnverifiedProposalHeight())
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
			ps := newPeerState(context.Background(), nil, nil)
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
			ps := newPeerState(context.Background(), nil, nil)
			ps.SetConcurrentReqs(tt.initialCount)

			ps.DecreaseConcurrentReqs(tt.decrease)

			got := ps.concurrentReqs.Load()
			assert.Equal(t, tt.expectedCount, got)
		})
	}
}

// Helper to create a test compact block
func newTestCompactBlock(height int64, round int32) *proptypes.CompactBlock {
	return &proptypes.CompactBlock{
		Proposal: types.Proposal{
			Height: height,
			Round:  round,
		},
	}
}

func TestPeerState_StoreUnverifiedProposal(t *testing.T) {
	t.Run("store single proposal", func(t *testing.T) {
		ps := newTestPeerState()
		cb := newTestCompactBlock(10, 0)

		stored := ps.StoreUnverifiedProposal(cb)
		require.True(t, stored)

		got := ps.GetUnverifiedProposal(10)
		require.NotNil(t, got)
		assert.Equal(t, int64(10), got.Proposal.Height)
	})

	t.Run("replace proposal for same height", func(t *testing.T) {
		ps := newTestPeerState()
		cb1 := newTestCompactBlock(10, 0)
		cb2 := newTestCompactBlock(10, 1) // Same height, different round

		ps.StoreUnverifiedProposal(cb1)
		ps.StoreUnverifiedProposal(cb2)

		got := ps.GetUnverifiedProposal(10)
		require.NotNil(t, got)
		assert.Equal(t, int32(1), got.Proposal.Round, "should be replaced with newer proposal")
	})

	t.Run("store up to capacity", func(t *testing.T) {
		ps := newTestPeerState()

		// Fill cache to capacity
		for h := int64(1); h <= MaxUnverifiedProposals; h++ {
			stored := ps.StoreUnverifiedProposal(newTestCompactBlock(h, 0))
			require.True(t, stored, "should store height %d", h)
		}

		// All should be retrievable
		for h := int64(1); h <= MaxUnverifiedProposals; h++ {
			got := ps.GetUnverifiedProposal(h)
			require.NotNil(t, got, "should have height %d", h)
		}
	})

	t.Run("evict highest when full and lower height arrives", func(t *testing.T) {
		ps := newTestPeerState()

		// Fill cache with heights 100 to 100+MaxUnverifiedProposals-1
		startHeight := int64(100)
		for h := startHeight; h < startHeight+int64(MaxUnverifiedProposals); h++ {
			ps.StoreUnverifiedProposal(newTestCompactBlock(h, 0))
		}

		maxHeight := startHeight + int64(MaxUnverifiedProposals) - 1

		// Now store height 5 - should evict highest
		stored := ps.StoreUnverifiedProposal(newTestCompactBlock(5, 0))
		require.True(t, stored, "lower height should be stored")

		// Height 5 should exist
		got := ps.GetUnverifiedProposal(5)
		require.NotNil(t, got, "height 5 should exist")

		// Highest height should be evicted
		got = ps.GetUnverifiedProposal(maxHeight)
		require.Nil(t, got, "highest height should be evicted")

		// Other heights should still exist
		for h := startHeight; h < maxHeight; h++ {
			got = ps.GetUnverifiedProposal(h)
			require.NotNil(t, got, "height %d should still exist", h)
		}
	})

	t.Run("reject higher height when full with lower heights", func(t *testing.T) {
		ps := newTestPeerState()

		// Fill cache with heights 1 to MaxUnverifiedProposals
		for h := int64(1); h <= int64(MaxUnverifiedProposals); h++ {
			ps.StoreUnverifiedProposal(newTestCompactBlock(h, 0))
		}

		// Try to store a height higher than max cached - should be rejected
		highHeight := int64(MaxUnverifiedProposals + 100)
		stored := ps.StoreUnverifiedProposal(newTestCompactBlock(highHeight, 0))
		require.False(t, stored, "higher height should be rejected when cache is full")

		// High height should not exist
		got := ps.GetUnverifiedProposal(highHeight)
		require.Nil(t, got)
	})
}

func TestPeerState_pruneUnverifiedProposals(t *testing.T) {
	t.Run("prune removes old heights", func(t *testing.T) {
		ps := newTestPeerState()

		// Add proposals for heights 5, 6, 7, 8
		for h := int64(5); h <= 8; h++ {
			ps.StoreUnverifiedProposal(newTestCompactBlock(h, 0))
		}

		// Prune with committedHeight=6 (should remove 5, 6, keep 7, 8)
		ps.prune(6)

		require.Nil(t, ps.GetUnverifiedProposal(5), "height 5 should be pruned")
		require.Nil(t, ps.GetUnverifiedProposal(6), "height 6 should be pruned")
		require.NotNil(t, ps.GetUnverifiedProposal(7), "height 7 should remain")
		require.NotNil(t, ps.GetUnverifiedProposal(8), "height 8 should remain")
	})

	t.Run("prune empty cache doesn't panic", func(t *testing.T) {
		ps := newTestPeerState()
		ps.prune(100) // Should not panic
	})

	t.Run("prune all entries", func(t *testing.T) {
		ps := newTestPeerState()

		for h := int64(1); h <= 5; h++ {
			ps.StoreUnverifiedProposal(newTestCompactBlock(h, 0))
		}

		ps.prune(10) // Prune everything

		for h := int64(1); h <= 5; h++ {
			require.Nil(t, ps.GetUnverifiedProposal(h), "height %d should be pruned", h)
		}
	})
}

func TestPeerState_DeleteUnverifiedProposal(t *testing.T) {
	ps := newTestPeerState()

	ps.StoreUnverifiedProposal(newTestCompactBlock(10, 0))
	require.NotNil(t, ps.GetUnverifiedProposal(10))

	ps.DeleteUnverifiedProposal(10)
	require.Nil(t, ps.GetUnverifiedProposal(10))

	// Deleting non-existent proposal doesn't panic
	ps.DeleteUnverifiedProposal(999)
}

// TestPeerState_pruneRemainingRequestsWithoutState checks that request
// counters set without a part state entry for their height are still pruned.
func TestPeerState_pruneRemainingRequestsWithoutState(t *testing.T) {
	ps := newTestPeerState()
	for h := int64(1); h <= 5; h++ {
		ps.SetRemainingRequests(h, 0, 1)
	}

	ps.prune(5)

	ps.mtx.RLock()
	defer ps.mtx.RUnlock()
	require.Empty(t, ps.state)
	require.Len(t, ps.remainingRequests, 1)
	require.Contains(t, ps.remainingRequests, int64(5))
}

// TestPeerState_capsRoundsPerHeight checks that no caller can grow a peer's
// part state or request counters past MaxPeerStateRoundsPerHeight rounds at
// one height, and that the lowest rounds are kept.
func TestPeerState_capsRoundsPerHeight(t *testing.T) {
	ps := newTestPeerState()
	const height = int64(3)
	for r := int32(1); r <= 1000; r++ {
		ps.Initialize(height, r, 4)
		ps.AddHaves(height, r, bits.NewBitArray(4))
		ps.AddWants(height, r, bits.NewBitArray(4))
		ps.AddRequests(height, r, bits.NewBitArray(4))
		ps.SetRemainingRequests(height, r, 1)
	}

	ps.mtx.RLock()
	require.Len(t, ps.state[height], MaxPeerStateRoundsPerHeight)
	require.Len(t, ps.remainingRequests[height], MaxPeerStateRoundsPerHeight)
	ps.mtx.RUnlock()
	_, has := ps.GetWants(height, 1)
	require.True(t, has, "lowest round must be kept")
	_, has = ps.GetWants(height, 1000)
	require.False(t, has, "round above the cap must be refused")
	require.False(t, ps.SetRemainingRequests(height, 1000, 1))

	// A round below all tracked ones evicts the highest.
	highest := int32(MaxPeerStateRoundsPerHeight)
	ps.AddWants(height, 0, bits.NewBitArray(4))
	require.True(t, ps.SetRemainingRequests(height, 0, 1))
	_, has = ps.GetWants(height, 0)
	require.True(t, has)
	_, has = ps.GetWants(height, highest)
	require.False(t, has)
	require.Zero(t, ps.GetRemainingRequests(height, highest))
	ps.mtx.RLock()
	require.Len(t, ps.state[height], MaxPeerStateRoundsPerHeight)
	require.Len(t, ps.remainingRequests[height], MaxPeerStateRoundsPerHeight)
	ps.mtx.RUnlock()

	// Updating a tracked round still works when the height is full.
	require.NoError(t, ps.SetWant(height, 1, 2, true))
	require.True(t, ps.WantsPart(height, 1, 2))
}

// TestPeerState_capsHeights checks that no caller can grow a peer's part
// state or request counters past MaxPeerStateHeights heights, and that the
// lowest heights are kept.
func TestPeerState_capsHeights(t *testing.T) {
	ps := newTestPeerState()
	for h := int64(2); h <= 1000; h++ {
		ps.AddWants(h, 0, bits.NewBitArray(4))
		ps.SetRemainingRequests(h, 0, 1)
	}

	ps.mtx.RLock()
	require.Len(t, ps.state, MaxPeerStateHeights)
	require.Len(t, ps.remainingRequests, MaxPeerStateHeights)
	ps.mtx.RUnlock()
	_, has := ps.GetWants(2, 0)
	require.True(t, has, "lowest height must be kept")
	_, has = ps.GetWants(1000, 0)
	require.False(t, has, "height above the cap must be refused")

	// A height below all tracked ones evicts the highest.
	highest := int64(1 + MaxPeerStateHeights)
	ps.AddWants(1, 0, bits.NewBitArray(4))
	require.True(t, ps.SetRemainingRequests(1, 0, 1))
	_, has = ps.GetWants(1, 0)
	require.True(t, has)
	_, has = ps.GetWants(highest, 0)
	require.False(t, has)
	require.Zero(t, ps.GetRemainingRequests(highest, 0))

	// Pruning frees room for new heights.
	ps.prune(1000)
	ps.AddWants(1000, 0, bits.NewBitArray(4))
	_, has = ps.GetWants(1000, 0)
	require.True(t, has)
}
