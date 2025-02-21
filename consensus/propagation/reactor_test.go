package propagation

import (
	"github.com/tendermint/tendermint/proto/tendermint/version"
	"testing"
	"time"

	dbm "github.com/cometbft/cometbft-db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	cfg "github.com/tendermint/tendermint/config"
	proptypes "github.com/tendermint/tendermint/consensus/propagation/types"
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

func TestHandleHavesAndWantsAndRecoveryParts(t *testing.T) {
	reactors, _ := testBlockPropReactors(3)
	reactor1 := reactors[0]
	reactor2 := reactors[1]
	reactor3 := reactors[2]

	// adding the proposal manually so the haves/wants and recovery
	// parts are not rejected.
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
	added, _, _ = reactor3.AddProposal(&types2.Proposal{
		BlockID: types2.BlockID{
			Hash:          nil,
			PartSetHeader: types2.PartSetHeader{Total: 30},
		},
		Height: 10,
		Round:  1,
	})
	require.True(t, added)
	proof := merkle.Proof{LeafHash: cmtrand.Bytes(32)}

	// reactor 1 will receive haves from reactor 2
	reactor1.handleHaves(
		reactor2.self,
		&proptypes.HaveParts{
			Height: 10,
			Round:  1,
			Parts: []proptypes.PartMetaData{
				{Index: 2, Proof: proof},
				{Index: 3, Proof: proof},
				{Index: 4, Proof: proof},
			},
		},
		true,
	)

	haves, has := reactor1.getPeer(reactor2.self).GetHaves(10, 1)
	assert.True(t, has)
	assert.Equal(t, int64(10), haves.Height)
	assert.Equal(t, int32(1), haves.Round)
	assert.Contains(t, haves.Parts, proptypes.PartMetaData{Index: 2, Proof: proof})
	assert.Contains(t, haves.Parts, proptypes.PartMetaData{Index: 3, Proof: proof})
	assert.Contains(t, haves.Parts, proptypes.PartMetaData{Index: 4, Proof: proof})

	time.Sleep(500 * time.Millisecond)

	// reactor 1 will gossip the haves with reactor 3
	// check if the third reactor received the haves
	r3State := reactor3.getPeer(reactor1.self)
	require.NotNil(t, r3State)

	r3Haves, r3Has := r3State.GetHaves(10, 1)
	assert.True(t, r3Has)
	assert.Contains(t, r3Haves.Parts, proptypes.PartMetaData{Index: 3, Proof: proof})
	assert.Contains(t, r3Haves.Parts, proptypes.PartMetaData{Index: 4, Proof: proof})

	// since reactor 3 received the haves from reactor 1,
	// it will send back a want.
	// check if reactor 1 received the wants
	r1Want, r1Has := reactor1.getPeer(reactor3.self).GetWants(10, 1)
	assert.True(t, r1Has)
	assert.Equal(t, int64(10), r1Want.Height)
	assert.Equal(t, int32(1), r1Want.Round)

	// add the recovery part to the reactor 1.
	randomData := cmtrand.Bytes(10)
	reactor1.handleRecoveryPart(reactor2.self, &proptypes.RecoveryPart{
		Height: 10,
		Round:  1,
		Index:  2,
		Data:   randomData,
	})

	time.Sleep(500 * time.Millisecond)

	// check if reactor 3 received the recovery part.
	_, parts, _, found := reactor3.GetProposal(10, 1)
	assert.True(t, found)
	assert.Equal(t, uint32(1), parts.Count())
	assert.Equal(t, randomData, parts.GetPart(2).Bytes.Bytes())
}

func TestCatchup(t *testing.T) {
	reactors, _ := testBlockPropReactors(2)
	reactor1 := reactors[0]
	reactor2 := reactors[1]

	time.Sleep(time.Second)
	// add block meta of height 9
	block, parts := makeBlock(9)
	reactor1.store.SaveBlock(block, parts, block.LastCommit)
	block, parts = makeBlock(10)
	reactor1.store.SaveBlock(block, parts, block.LastCommit)
	//reactor1.store.SaveBlock(&types2.Block{}, &types2.PartSet{}, &types2.Commit{})
	// set reactor 1 proposal to height 10
	added, _, _ := reactor1.AddProposal(&types2.Proposal{
		BlockID: types2.BlockID{
			Hash:          nil,
			PartSetHeader: types2.PartSetHeader{Total: 30},
		},
		Height: 10,
		Round:  2,
	})
	require.True(t, added)

	// set reactor 2 proposal to height 9
	added, _, _ = reactor2.AddProposal(&types2.Proposal{
		BlockID: types2.BlockID{
			Hash:          nil,
			PartSetHeader: types2.PartSetHeader{Total: 30},
		},
		Height: 9,
		Round:  0,
	})
	require.True(t, added)
	proof := merkle.Proof{LeafHash: cmtrand.Bytes(32)}

	// reactor 2 will receive the block 10 round 2 proposal from reactor 1
	// so it will send him the wants for block 9 and for block 10 rounds 0 and 1
	reactor2.handleProposal(
		reactor1.self,
		&proptypes.Proposal{
			Proposal: &types2.Proposal{
				BlockID: types2.BlockID{
					Hash:          nil,
					PartSetHeader: types2.PartSetHeader{Total: 30},
				},
				Height: 10,
				Round:  2,
			},
		},
	)

	time.Sleep(50000 * time.Millisecond)

	// check if the reactor received the wants
	haves, has := reactor1.getPeer(reactor2.self).GetWants(10, 1)
	haves, has = reactor1.getPeer(reactor2.self).GetWants(10, 0)
	haves, has = reactor1.getPeer(reactor2.self).GetWants(9, 0)
	assert.True(t, has)
	assert.Equal(t, int64(10), haves.Height)
	assert.Equal(t, int32(1), haves.Round)
	assert.Contains(t, haves.Parts, proptypes.PartMetaData{Index: 2, Proof: proof})
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

func TestBroadcastProposal(t *testing.T) {
	reactors, _ := testBlockPropReactors(3)
	reactor1 := reactors[0]
	reactor2 := reactors[1]
	reactor3 := reactors[2]

	blockID := types2.BlockID{
		Hash:          cmtrand.Bytes(32),
		PartSetHeader: types2.PartSetHeader{Total: 30, Hash: cmtrand.Bytes(32)},
	}

	compBlock := types2.CompactBlock{
		BpHash:    cmtrand.Bytes(32),
		Height:    1,
		Round:     0,
		Blobs:     []*types2.TxMetaData{},
		Signature: cmtrand.Bytes(64),
		LastLen:   100,
	}

	proposal := &proptypes.Proposal{
		Proposal: types2.NewProposal(1, 0, 0, blockID, compBlock),
	}
	proposal.Signature = cmtrand.Bytes(64)

	reactor1.handleProposal(reactor2.self, proposal)

	time.Sleep(500 * time.Millisecond)

	_, _, _, has := reactor2.GetProposal(1, 0)
	assert.True(t, has)
	_, _, _, has = reactor3.GetProposal(1, 0)
	assert.True(t, has)
}

func createBitArray(size int, indices []int) *bits.BitArray {
	ba := bits.NewBitArray(size)
	for _, index := range indices {
		ba.SetIndex(index, true)
	}
	return ba
}

func makeBlock(height int64) (*types2.Block, *types2.PartSet) {
	// Build base block with block data.

	blockID := types2.BlockID{
		Hash:          cmtrand.Bytes(32),
		PartSetHeader: types2.PartSetHeader{Total: 30, Hash: cmtrand.Bytes(32)},
	}

	lastCommit := types2.Commit{
		Height:  height - 1,
		BlockID: blockID,
	}

	block := types2.MakeBlock(height, types2.Data{Txs: makeTxs(10)}, &lastCommit, []types2.Evidence{})

	// Set time.
	timestamp := time.Now()

	// Fill rest of header with state data.
	block.Header.Populate(
		version.Consensus{Block: 11, App: 1}, "test",
		timestamp, block.LastBlockID,
		cmtrand.Bytes(32), cmtrand.Bytes(32),
		cmtrand.Bytes(32), cmtrand.Bytes(32), cmtrand.Bytes(32),
		cmtrand.Bytes(20),
	)
	ops, _ := block.MakePartSet(types2.BlockPartSizeBytes)
	return block, ops
}

func makeTxs(size int) (txs []types2.Tx) {
	for i := 0; i < 10; i++ {
		txs = append(txs, types2.Tx(cmtrand.Bytes(size)))
	}
	return txs
}
