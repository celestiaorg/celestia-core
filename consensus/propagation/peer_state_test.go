package propagation

import (
	"testing"

	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/p2p/mock"

	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/consensus/propagation/types"
	"github.com/cometbft/cometbft/libs/bits"
)

// newTestPeerState is a helper to create a PeerState with a mock peer.
func newTestPeerState() *PeerState {
	peer := mock.Peer{}
	return newPeerState(&peer, log.NewNopLogger())
}

func TestPeerState_SetHaves(t *testing.T) {
	tests := []struct {
		name   string
		height int64
		round  int32
		input  *types.HaveParts
	}{
		{
			name:   "basic set haves",
			height: 10,
			round:  2,
			input: &types.HaveParts{
				Height: 10,
				Round:  2,
				Parts: []types.PartMetaData{
					{Index: 0, Hash: []byte("hash0")},
					{Index: 1, Hash: []byte("hash1")},
				},
			},
		},
		{
			name:   "another set haves different height/round",
			height: 15,
			round:  1,
			input: &types.HaveParts{
				Height: 15,
				Round:  1,
				Parts:  []types.PartMetaData{},
			},
		},
	}

	for _, tt := range tests {
		tt := tt // pin
		t.Run(tt.name, func(t *testing.T) {
			ps := newTestPeerState()
			ps.SetHaves(tt.height, tt.round, tt.input)

			gotHaves, ok := ps.GetHaves(tt.height, tt.round)
			require.True(t, ok, "GetHaves should indicate presence of Haves data")
			require.Equal(t, tt.input.Height, gotHaves.Height)
			require.Equal(t, tt.input.Round, gotHaves.Round)
			require.Equal(t, len(tt.input.Parts), len(gotHaves.Parts))
		})
	}
}

func TestPeerState_SetWants(t *testing.T) {
	tests := []struct {
		name     string
		wants    *types.WantParts
		expSize  int
		expRound int32
	}{
		{
			name: "simple wants",
			wants: &types.WantParts{
				Parts:  bits.NewBitArray(4),
				Height: 20,
				Round:  0,
			},
			expSize:  4,
			expRound: 0,
		},
		{
			name: "larger wants set",
			wants: &types.WantParts{
				Parts:  bits.NewBitArray(10),
				Height: 5,
				Round:  3,
			},
			expSize:  10,
			expRound: 3,
		},
	}

	for _, tt := range tests {
		tt := tt // pin
		t.Run(tt.name, func(t *testing.T) {
			ps := newTestPeerState()
			ps.SetWants(tt.wants)

			gotWants, ok := ps.GetWants(tt.wants.Height, tt.wants.Round)
			require.True(t, ok, "GetWants should return true after SetWants")
			require.Equal(t, tt.expSize, gotWants.Parts.Size())
			require.Equal(t, tt.expRound, gotWants.Round)
		})
	}
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
			ps.SetRequests(tt.height, tt.round, tt.requestBits)

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

func TestPeerState_SetRequest(t *testing.T) {
	tests := []struct {
		name   string
		height int64
		round  int32
		part   int
	}{
		{
			name:   "basic request",
			height: 7,
			round:  1,
			part:   2,
		},
		{
			name:   "out of range part? -> no effect if state uninitialized",
			height: 10,
			round:  2,
			part:   5,
		},
	}

	for _, tt := range tests {
		tt := tt // pin
		t.Run(tt.name, func(t *testing.T) {
			ps := newTestPeerState()

			// Initialize only if needed
			if tt.name != "out of range part? -> no effect if state uninitialized" {
				ps.SetRequests(tt.height, tt.round, bits.NewBitArray(6)) // size=6
			}
			ps.SetRequest(tt.height, tt.round, tt.part)

			got, ok := ps.GetRequests(tt.height, tt.round)
			if tt.name == "out of range part? -> no effect if state uninitialized" {
				require.False(t, ok, "No state created -> nothing stored")
				return
			}
			require.True(t, ok, "Should have state for requests")
			require.True(t, got.GetIndex(tt.part), "The 'part' bit should be set to true")
		})
	}
}

func TestPeerState_SetHave(t *testing.T) {
	tests := []struct {
		name   string
		height int64
		round  int32
		part   int
	}{
		{
			name:   "basic set have",
			height: 10,
			round:  1,
			part:   0,
		},
		{
			name:   "no prior state -> do nothing",
			height: 5,
			round:  0,
			part:   2,
		},
	}

	for _, tt := range tests {
		tt := tt // pin
		t.Run(tt.name, func(t *testing.T) {
			ps := newTestPeerState()
			// Initialize if we expect to have some state
			if tt.name == "basic set have" {
				// must create haves array
				haveParts := &types.HaveParts{
					Height: tt.height,
					Round:  tt.round,
					Parts:  make([]types.PartMetaData, 3), // size=3
				}
				ps.SetHaves(tt.height, tt.round, haveParts)
			}

			ps.SetHave(tt.height, tt.round, tt.part)

			got, ok := ps.GetHaves(tt.height, tt.round)
			if tt.name == "no prior state -> do nothing" {
				require.False(t, ok)
				return
			}
			// We expect the bit to be set
			require.True(t, ok)
			require.True(t, got.GetIndex(uint32(tt.part)), "Part index should be set in Haves")
		})
	}
}

func TestPeerState_SetWant(t *testing.T) {
	tests := []struct {
		name   string
		height int64
		round  int32
		part   int
		want   bool
	}{
		{
			name:   "set want bit true",
			height: 10,
			round:  1,
			part:   3,
			want:   true,
		},
		{
			name:   "set want bit false with no prior state",
			height: 5,
			round:  0,
			part:   2,
			want:   false,
		},
	}

	for _, tt := range tests {
		tt := tt // pin
		t.Run(tt.name, func(t *testing.T) {
			ps := newTestPeerState()
			// Only create the wants data if we expect a valid state
			if tt.name == "set want bit true" {
				wants := &types.WantParts{
					Parts:  bits.NewBitArray(5), // size=5
					Height: tt.height,
					Round:  tt.round,
				}
				ps.SetWants(wants)
			}

			ps.SetWant(tt.height, tt.round, tt.part, tt.want)

			got, ok := ps.GetWants(tt.height, tt.round)
			if tt.name == "set want bit false with no prior state" {
				require.False(t, ok)
				return
			}
			require.True(t, ok)
			require.Equal(t, tt.want, got.Parts.GetIndex(tt.part))
		})
	}
}

func TestPeerState_WantsPart(t *testing.T) {
	tests := []struct {
		name        string
		height      int64
		round       int32
		part        uint32
		preSetWants bool
		wantResult  bool
	}{
		{
			name:        "wants part is true",
			height:      10,
			round:       2,
			part:        1,
			preSetWants: true,
			wantResult:  true,
		},
		{
			name:        "wants part is false - no prior state",
			height:      5,
			round:       1,
			part:        0,
			preSetWants: false,
			wantResult:  false,
		},
		{
			name:        "wants part is false - bit not set",
			height:      8,
			round:       3,
			part:        5,
			preSetWants: true,
			wantResult:  false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			ps := newTestPeerState()
			if tt.preSetWants {
				wants := &types.WantParts{
					Parts:  bits.NewBitArray(6), // size=6
					Height: tt.height,
					Round:  tt.round,
				}
				ps.SetWants(wants)
				// If we want the test to pass for the part, we set that bit:
				if tt.wantResult {
					ps.SetWant(tt.height, tt.round, int(tt.part), true)
				}
			}

			got := ps.WantsPart(tt.height, tt.round, tt.part)
			require.Equal(t, tt.wantResult, got)
		})
	}
}

func TestPeerState_DeleteHeight(t *testing.T) {
	ps := newTestPeerState()
	heightToDelete := int64(10)

	// Create some data at height=10, round=1
	ps.SetHaves(heightToDelete, 1, &types.HaveParts{
		Height: heightToDelete,
		Round:  1,
		Parts:  []types.PartMetaData{{Index: 0, Hash: []byte("hash0")}},
	})
	// Also create data at a different height
	ps.SetHaves(20, 0, &types.HaveParts{
		Height: 20,
		Round:  0,
		Parts:  []types.PartMetaData{{Index: 1, Hash: []byte("hash1")}},
	})

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

	// Populate test data
	for h := int64(10); h <= 13; h++ {
		for r := int32(0); r < 3; r++ {
			ps.SetHaves(h, r, &types.HaveParts{
				Height: h,
				Round:  r,
				Parts:  []types.PartMetaData{{Index: 0, Hash: []byte("dummy")}},
			})
		}
	}

	// Prune
	ps.prune(13, 2, 1)

	// Height < 11 => remove
	_, okH10r0 := ps.GetHaves(10, 0)
	require.False(t, okH10r0, "height=10 should be removed entirely")

	// Height=11 => keep only round >= 12 => that means no rounds at all (0..2 < 12 => all removed)
	_, okH11r0 := ps.GetHaves(11, 0)
	require.False(t, okH11r0, "all rounds at height=11 removed")

	_, okH11r2 := ps.GetHaves(11, 2)
	require.False(t, okH11r2, "all rounds at height=11 removed")

	// Height=12 => keep only round >= 12 => that means round=0..2 <12 => removed
	_, okH12r2 := ps.GetHaves(12, 2)
	require.False(t, okH12r2, "all rounds at height=12 removed")

	// Height=13 => do nothing, keep all rounds
	for r := int32(0); r < 3; r++ {
		_, ok := ps.GetHaves(13, r)
		require.True(t, ok, "height=13 round=%d should remain", r)
	}
}
