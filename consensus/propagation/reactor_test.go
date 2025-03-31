package propagation

import (
	"path/filepath"
	"strconv"
	"testing"
	"time"

	dbm "github.com/cometbft/cometbft-db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	cfg "github.com/tendermint/tendermint/config"
	proptypes "github.com/tendermint/tendermint/consensus/propagation/types"
	"github.com/tendermint/tendermint/libs/bits"
	"github.com/tendermint/tendermint/libs/log"
	cmtrand "github.com/tendermint/tendermint/libs/rand"
	"github.com/tendermint/tendermint/p2p"
	"github.com/tendermint/tendermint/p2p/mock"
	"github.com/tendermint/tendermint/pkg/trace"
	"github.com/tendermint/tendermint/state"
	"github.com/tendermint/tendermint/store"
	"github.com/tendermint/tendermint/types"
)

func newPropagationReactor(s *p2p.Switch, _ trace.Tracer) *Reactor {
	blockStore := store.NewBlockStore(dbm.NewMemDB())
	blockPropR := NewReactor(s.NetAddress().ID, trace.NoOpTracer(), blockStore, &mockMempool{txs: make(map[types.TxKey]*types.CachedTx)})
	blockPropR.SetSwitch(s)

	return blockPropR
}

func testBlockPropReactors(n int, p2pCfg *cfg.P2PConfig) ([]*Reactor, []*p2p.Switch) {
	return createTestReactors(n, p2pCfg, false, "")
}

func createTestReactors(n int, p2pCfg *cfg.P2PConfig, tracer bool, traceDir string) ([]*Reactor, []*p2p.Switch) {
	reactors := make([]*Reactor, n)
	switches := make([]*p2p.Switch, n)

	p2p.MakeConnectedSwitches(p2pCfg, n, func(i int, s *p2p.Switch) *p2p.Switch {
		var (
			tr  trace.Tracer
			err error
		)
		if !tracer {
			tr = trace.NoOpTracer()
		} else {
			dconfig := cfg.DefaultConfig()
			dconfig.SetRoot(filepath.Join(traceDir, strconv.Itoa(i)))
			tr, err = trace.NewLocalTracer(dconfig, log.NewNopLogger(), "test", string(s.NetAddress().ID))
			if err != nil {
				panic(err)
			}
		}
		reactors[i] = newPropagationReactor(s, tr)
		s.AddReactor("BlockProp", reactors[i])
		switches = append(switches, s)
		return s
	},
		p2p.Connect2Switches,
	)

	return reactors, switches
}

func TestCountRequests(t *testing.T) {
	reactors, _ := testBlockPropReactors(1, cfg.DefaultP2PConfig())
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
	peer1State.AddRequests(10, 0, array)

	peer2State := reactor.getPeer(peer2.ID())
	// peer2 requests part=0 and part=2 and part=3  at height=10, round=0
	array2 := bits.NewBitArray(3)
	array2.SetIndex(0, true)
	array2.SetIndex(2, true)
	array2.SetIndex(3, true)
	peer2State.AddRequests(10, 0, array2)

	// peer3 doesn't request anything

	// count requests part=0 at height=10, round=0
	part0Round0Height10RequestsCount := reactor.countRequests(10, 0, 0)
	assert.Equal(t, 2, len(part0Round0Height10RequestsCount))

	// count requests part=3 at height=10, round=0
	part3Round0Height10RequestsCount := reactor.countRequests(10, 0, 2)
	assert.Equal(t, 1, len(part3Round0Height10RequestsCount))
}

func TestHandleHavesAndWantsAndRecoveryParts(t *testing.T) {
	reactors, _ := testBlockPropReactors(3, cfg.DefaultP2PConfig())
	reactor1 := reactors[0]
	reactor2 := reactors[1]
	reactor3 := reactors[2]

	randomData := cmtrand.Bytes(1000)
	ps := types.NewPartSetFromData(randomData, types.BlockPartSizeBytes)
	pse, lastLen, err := types.Encode(ps, types.BlockPartSizeBytes)
	require.NoError(t, err)
	psh := ps.Header()
	pseh := pse.Header()

	hashes := extractHashes(ps, pse)
	proofs := extractProofs(ps, pse)

	baseCompactBlock := &proptypes.CompactBlock{
		BpHash:    pseh.Hash,
		Signature: cmtrand.Bytes(64),
		LastLen:   uint32(lastLen),
		Blobs: []proptypes.TxMetaData{
			{Hash: cmtrand.Bytes(32)},
			{Hash: cmtrand.Bytes(32)},
		},
		PartsHashes: hashes,
	}

	baseCompactBlock.SetProofCache(proofs)

	height, round := int64(10), int32(1)

	// adding the proposal manually so the haves/wants and recovery
	// parts are not rejected.
	p := types.Proposal{
		BlockID: types.BlockID{
			Hash:          cmtrand.Bytes(32),
			PartSetHeader: psh,
		},
		Height: height,
		Round:  round,
	}
	baseCompactBlock.Proposal = p

	added := reactor1.AddProposal(baseCompactBlock)
	require.True(t, added)
	added = reactor2.AddProposal(baseCompactBlock)
	require.True(t, added)
	added = reactor3.AddProposal(baseCompactBlock)
	require.True(t, added)

	// reactor 1 will receive haves from reactor 2
	reactor1.handleHaves(
		reactor2.self,
		&proptypes.HaveParts{
			Height: height,
			Round:  round,
			Parts: []proptypes.PartMetaData{
				{Index: 0},
			},
		},
		false,
	)

	haves, has := reactor1.getPeer(reactor2.self).GetHaves(height, round)
	assert.True(t, has)
	require.True(t, haves.GetIndex(0))

	time.Sleep(400 * time.Millisecond)

	r3State := reactor3.getPeer(reactor1.self)
	require.NotNil(t, r3State)

	r3Haves, r3Has := r3State.GetHaves(height, round)
	assert.True(t, r3Has)
	require.True(t, r3Haves.GetIndex(0))

	reactor1.handleRecoveryPart(reactor2.self, &proptypes.RecoveryPart{
		Height: height,
		Round:  round,
		Index:  0,
		Data:   ps.GetPart(0).Bytes.Bytes(),
	})

	time.Sleep(500 * time.Millisecond)

	// check if reactor 3 received the recovery part.
	_, parts, found := reactor3.GetProposal(10, 1)
	require.True(t, found)
	require.Equal(t, uint32(1), parts.Count())
	require.Equal(t, randomData, parts.GetPart(0).Bytes.Bytes())

	// check to see if the parity data was generated after receiveing the first part.
	_, combined, _, has := reactor3.getAllState(height, round, true)
	assert.True(t, has)
	assert.True(t, combined.IsComplete())
	parityPart, has := combined.GetPart(1)
	assert.True(t, has)
	assert.NotNil(t, parityPart)
}

func TestInvalidPart(t *testing.T) {
	reactors, _ := testBlockPropReactors(2, cfg.DefaultP2PConfig())
	reactor1 := reactors[0]
	reactor2 := reactors[1]

	randomData := cmtrand.Bytes(1000)
	ps := types.NewPartSetFromData(randomData, types.BlockPartSizeBytes)
	pse, lastLen, err := types.Encode(ps, types.BlockPartSizeBytes)
	require.NoError(t, err)
	psh := ps.Header()
	pseh := pse.Header()

	hashes := extractHashes(ps, pse)
	proofs := extractProofs(ps, pse)

	baseCompactBlock := &proptypes.CompactBlock{
		BpHash:    pseh.Hash,
		Signature: cmtrand.Bytes(64),
		LastLen:   uint32(lastLen),
		Blobs: []proptypes.TxMetaData{
			{Hash: cmtrand.Bytes(32)},
			{Hash: cmtrand.Bytes(32)},
		},
		PartsHashes: hashes,
	}

	baseCompactBlock.SetProofCache(proofs)

	height, round := int64(10), int32(1)

	// adding the proposal manually so the haves/wants and recovery
	// parts are not rejected.
	p := types.Proposal{
		BlockID: types.BlockID{
			Hash:          cmtrand.Bytes(32),
			PartSetHeader: psh,
		},
		Height: height,
		Round:  round,
	}
	baseCompactBlock.Proposal = p

	added := reactor1.AddProposal(baseCompactBlock)
	require.True(t, added)
	added = reactor2.AddProposal(baseCompactBlock)
	require.True(t, added)

	// reactor 1 will receive haves from reactor 2
	reactor1.handleHaves(
		reactor2.self,
		&proptypes.HaveParts{
			Height: height,
			Round:  round,
			Parts: []proptypes.PartMetaData{
				{Index: 0},
			},
		},
		false,
	)

	haves, has := reactor1.getPeer(reactor2.self).GetHaves(height, round)
	assert.True(t, has)
	require.True(t, haves.GetIndex(0))

	time.Sleep(400 * time.Millisecond)

	ps.GetPart(0).Bytes[0] = 0x12
	ps.GetPart(0).Bytes[1] = 0x12
	ps.GetPart(0).Bytes[2] = 0x12

	reactor1.handleRecoveryPart(reactor2.self, &proptypes.RecoveryPart{
		Height: height,
		Round:  round,
		Index:  0,
		Data:   ps.GetPart(0).Bytes.Bytes(),
	})

	time.Sleep(500 * time.Millisecond)

	// check if reactor 3 received the recovery part.
	_, parts, found := reactor2.GetProposal(10, 1)
	require.True(t, found)
	badPart := parts.GetPart(0)
	require.Nil(t, badPart)
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

// TestPropagationSmokeTest is a high level smoke test for 10 reactors to distrute 5 2MB
// blocks with some data already distributed via the mempool. The passing
// criteria is simply finishing.
func TestPropagationSmokeTest(t *testing.T) {
	p2pCfg := cfg.DefaultP2PConfig()
	p2pCfg.SendRate = 100000000
	p2pCfg.RecvRate = 110000000

	nodes := 10

	reactors, _ := createTestReactors(nodes, p2pCfg, false, "")

	cleanup, _, sm := state.SetupTestCase(t)
	t.Cleanup(func() {
		cleanup(t)
	})

	for i := int64(1); i < 5; i++ {
		prop, ps, block, metaData := createTestProposal(sm, int64(i), 2, 1000000)

		// predistribute portions of the block
		for _, tx := range block.Data.Txs {
			for j := 0; j < nodes/2; j++ {
				r := reactors[j]
				pool := r.mempool.(*mockMempool)
				pool.AddTx(tx)
			}
		}

		reactors[1].ProposeBlock(prop, ps, metaData)

		distributing := true
		for distributing {
			count := 0
			for _, r := range reactors {
				_, parts, _, has := r.getAllState(i, 0, false)
				if !has {
					continue
				}
				if !parts.IsComplete() {
					continue
				}

				count++

				if count == nodes {
					distributing = false
				}
			}
			time.Sleep(200 * time.Millisecond)
		}

		for _, r := range reactors {
			r.Prune(int64(i))
		}
	}

}

func createBitArray(size int, indices []int) *bits.BitArray {
	ba := bits.NewBitArray(size)
	for _, index := range indices {
		ba.SetIndex(index, true)
	}
	return ba
}
