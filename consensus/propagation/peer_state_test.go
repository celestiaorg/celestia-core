package propagation

import (
	"testing"

	"github.com/tendermint/tendermint/libs/log"
	"github.com/tendermint/tendermint/p2p/mock"

	"github.com/stretchr/testify/require"

	"github.com/tendermint/tendermint/libs/bits"
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
			ps.AddSentWants(tt.height, tt.round, tt.requestBits)

			gotRequests, ok := ps.GetSentWants(tt.height, tt.round)
			if tt.requestBits.Size() == 0 {
				// If requestBits had 0 size, we expect not to store anything.
				require.False(t, ok, "GetRequests should be false if sentWants is 0 size")
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
	ps.AddSentHaves(heightToDelete, 1, bm)
	// Also create data at a different height
	ps.AddSentHaves(20, 0, bm)

	// Now delete the data for height=10
	ps.DeleteHeight(heightToDelete)

	// Verify height=10 data is gone
	_, ok := ps.GetSentHaves(heightToDelete, 1)
	require.False(t, ok, "Expected no data for height=10 after DeleteHeight")

	// But height=20 data remains
	_, ok2 := ps.GetSentHaves(20, 0)
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
			ps.AddSentHaves(h, r, bm)
		}
	}

	// Prune
	ps.prune(height - 2)

	// Height < 11 => remove
	_, okH10r0 := ps.GetSentHaves(10, 0)
	require.False(t, okH10r0, "height=10 should be removed entirely")

	// Height=11 => keep only round >= 12 => that means no rounds at all (0..2 < 12 => all removed)
	_, okH11r0 := ps.GetSentHaves(11, 0)
	require.True(t, okH11r0, "all rounds at height=11 removed")

	_, okH11r2 := ps.GetSentHaves(11, 2)
	require.True(t, okH11r2, "all rounds at height=11 removed")

	// Height=12 => keep only round >= 12 => that means round=0..2 <12 => removed
	_, okH12r2 := ps.GetSentHaves(12, 2)
	require.True(t, okH12r2, "all rounds at height=12 removed")

	// Height=13 => do nothing, keep all rounds
	for r := int32(0); r < 3; r++ {
		_, ok := ps.GetSentHaves(13, r)
		require.True(t, ok, "height=13 round=%d should remain", r)
	}
}
