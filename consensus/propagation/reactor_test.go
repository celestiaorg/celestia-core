package propagation

import (
	"testing"
	"time"

	dbm "github.com/cometbft/cometbft-db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	cfg "github.com/tendermint/tendermint/config"
	"github.com/tendermint/tendermint/consensus/propagation/types"
	"github.com/tendermint/tendermint/crypto/merkle"
	"github.com/tendermint/tendermint/libs/bits"
	cmtrand "github.com/tendermint/tendermint/libs/rand"
	"github.com/tendermint/tendermint/p2p"
	"github.com/tendermint/tendermint/p2p/mock"
	"github.com/tendermint/tendermint/pkg/trace"
	"github.com/tendermint/tendermint/store"
	types2 "github.com/tendermint/tendermint/types"
)

func newPropagationReactor(s *p2p.Switch) *Reactor {
	blockStore := store.NewBlockStore(dbm.NewMemDB())
	blockPropR := NewReactor(s.NetAddress().ID, trace.NoOpTracer(), blockStore)
	blockPropR.SetSwitch(s)

	return blockPropR
}

func testBlockPropReactors(n int) ([]*Reactor, []*p2p.Switch) {
	reactors := make([]*Reactor, n)
	switches := make([]*p2p.Switch, n)

	p2pCfg := cfg.DefaultP2PConfig()

	p2p.MakeConnectedSwitches(p2pCfg, n, func(i int, s *p2p.Switch) *p2p.Switch {
		reactors[i] = newPropagationReactor(s)
		s.AddReactor("BlockProp", reactors[i])
		switches = append(switches, s)
		return s
	},
		p2p.Connect2Switches,
	)

	return reactors, switches
}

func TestCountRequests(t *testing.T) {
	reactors, _ := testBlockPropReactors(1)
	reactor := reactors[0]

	peer1 := mock.NewPeer(nil)
	reactor.AddPeer(peer1)
	peer2 := mock.NewPeer(nil)
	reactor.AddPeer(peer2)
	peer3 := mock.NewPeer(nil)
	reactor.AddPeer(peer3)

	peer1State := reactor.getPeer(peer1.ID())
	// peer1 requests part=0 at height=10, round=0
	array := bits.NewBitArray(3)
	array.SetIndex(0, true)
	peer1State.SetRequests(10, 0, array)

	peer2State := reactor.getPeer(peer2.ID())
	// peer2 requests part=0 and part=2 and part=3  at height=10, round=0
	array2 := bits.NewBitArray(3)
	array2.SetIndex(0, true)
	array2.SetIndex(2, true)
	array2.SetIndex(3, true)
	peer2State.SetRequests(10, 0, array2)

	// peer3 doesn't request anything

	// count requests part=0 at height=10, round=0
	part0Round0Height10RequestsCount := reactor.countRequests(10, 0, 0)
	assert.Equal(t, 2, len(part0Round0Height10RequestsCount))

	// count requests part=3 at height=10, round=0
	part3Round0Height10RequestsCount := reactor.countRequests(10, 0, 2)
	assert.Equal(t, 1, len(part3Round0Height10RequestsCount))
}

func TestHandleHavesAndWants(t *testing.T) {
	reactors, _ := testBlockPropReactors(3)
	reactor1 := reactors[0]
	reactor2 := reactors[1]

	peer1 := mock.NewPeer(nil)
	reactor1.AddPeer(peer1)
	added, _, _ := reactor1.AddProposal(&types2.Proposal{
		BlockID: types2.BlockID{
			Hash:          nil,
			PartSetHeader: types2.PartSetHeader{Total: 30},
		},
		Height: 10,
		Round:  1,
	})
	require.True(t, added)
	added, _, _ = reactor2.AddProposal(&types2.Proposal{
		BlockID: types2.BlockID{
			Hash:          nil,
			PartSetHeader: types2.PartSetHeader{Total: 30},
		},
		Height: 10,
		Round:  1,
	})
	require.True(t, added)
	proof := merkle.Proof{LeafHash: cmtrand.Bytes(32)}
	reactor1.handleHaves(
		peer1.ID(),
		&types.HaveParts{
			Height: 10,
			Round:  1,
			Parts: []types.PartMetaData{
				{Index: 2, Proof: proof},
				{Index: 3, Proof: proof},
				{Index: 4, Proof: proof},
			},
		},
		true,
	)

	haves, has := reactor1.getPeer(peer1.ID()).GetHaves(10, 1)
	assert.True(t, has)
	assert.Equal(t, int64(10), haves.Height)
	assert.Equal(t, int32(1), haves.Round)
	assert.Contains(t, haves.Parts, types.PartMetaData{Index: 2, Proof: proof})
	assert.Contains(t, haves.Parts, types.PartMetaData{Index: 3, Proof: proof})
	assert.Contains(t, haves.Parts, types.PartMetaData{Index: 4, Proof: proof})

	time.Sleep(500 * time.Millisecond)

	r2State := reactor2.getPeers()[0]
	require.NotNil(t, r2State)

	// check if the second reactor received the haves
	r2Haves, r2Has := r2State.GetHaves(10, 1)
	assert.True(t, r2Has)
	assert.Contains(t, r2Haves.Parts, types.PartMetaData{Index: 2, Proof: proof})
	assert.Contains(t, r2Haves.Parts, types.PartMetaData{Index: 3, Proof: proof})
	assert.Contains(t, r2Haves.Parts, types.PartMetaData{Index: 4, Proof: proof})

	// check if the peer received the wants
	r2Want, r2Has := r2State.GetWants(10, 1)
	assert.True(t, r2Has)
	assert.Equal(t, int64(10), r2Want.Height)
	assert.Equal(t, int32(1), r2Want.Round)
}

func TestChunkParts(t *testing.T) {
	tests := []struct {
		name       string
		bitArray   *bits.BitArray
		peerCount  int
		redundancy int
		expected   []*bits.BitArray
	}{
		{
			name:       "Basic case with redundancy",
			bitArray:   bits.NewBitArray(6),
			peerCount:  3,
			redundancy: 2,
			expected: []*bits.BitArray{
				createBitArray(6, []int{0, 1, 2, 3}),
				createBitArray(6, []int{0, 1, 4, 5}),
				createBitArray(6, []int{2, 3, 4, 5}),
			},
		},
		{
			name:       "No redundancy",
			bitArray:   bits.NewBitArray(6),
			peerCount:  3,
			redundancy: 1,
			expected: []*bits.BitArray{
				createBitArray(6, []int{0, 1}),
				createBitArray(6, []int{2, 3}),
				createBitArray(6, []int{4, 5}),
			},
		},
		{
			name:       "Full overlap",
			bitArray:   bits.NewBitArray(4),
			peerCount:  2,
			redundancy: 2,
			expected: []*bits.BitArray{
				createBitArray(4, []int{0, 1, 2, 3}),
				createBitArray(4, []int{0, 1, 2, 3}),
			},
		},
		{
			name:       "uneven",
			bitArray:   bits.NewBitArray(4),
			peerCount:  3,
			redundancy: 2,
			expected: []*bits.BitArray{
				createBitArray(4, []int{0, 1, 2, 3}),
				createBitArray(4, []int{0, 1, 2, 3}),
				createBitArray(4, []int{0, 1, 2, 3}),
			},
		},
		{
			name:       "uneven",
			bitArray:   bits.NewBitArray(4),
			peerCount:  9,
			redundancy: 1,
			expected: []*bits.BitArray{
				// TODO verify if this is the right result
				createBitArray(4, []int{0}),
				createBitArray(4, []int{1}),
				createBitArray(4, []int{2}),
				createBitArray(4, []int{3}),
				createBitArray(4, []int{0}),
				createBitArray(4, []int{1}),
				createBitArray(4, []int{2}),
				createBitArray(4, []int{3}),
				createBitArray(4, []int{0}),
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ba := tc.bitArray
			ba.Fill()
			result := chunkParts(ba, tc.peerCount, tc.redundancy)
			require.Equal(t, len(tc.expected), len(result))

			for i, exp := range tc.expected {
				require.Equal(t, exp.String(), result[i].String(), i)
			}
		})
	}
}

func createBitArray(size int, indices []int) *bits.BitArray {
	ba := bits.NewBitArray(size)
	for _, index := range indices {
		ba.SetIndex(index, true)
	}
	return ba
}
