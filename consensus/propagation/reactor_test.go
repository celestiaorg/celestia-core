package propagation

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	proptypes "github.com/cometbft/cometbft/consensus/propagation/types"
	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/state"
	"github.com/cometbft/cometbft/types"

	"github.com/cometbft/cometbft/privval"
	"github.com/cometbft/cometbft/proto/tendermint/propagation"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	dbm "github.com/cometbft/cometbft-db"
	cfg "github.com/cometbft/cometbft/config"
	"github.com/cometbft/cometbft/crypto/merkle"
	"github.com/cometbft/cometbft/libs/bits"
	cmtrand "github.com/cometbft/cometbft/libs/rand"
	"github.com/cometbft/cometbft/libs/trace"
	"github.com/cometbft/cometbft/p2p"
	"github.com/cometbft/cometbft/p2p/mock"
	"github.com/cometbft/cometbft/store"
)

const (
	TestChainID = "test"
)

var (
	mockPrivVal = types.NewMockPV()
	mockPrivKey = mockPrivVal.PrivKey
	mockPubKey  = mockPrivKey.PubKey()
)

// newPropagationReactor creates a propagation reactor using the provided
// privval sign and that key to verify proposals.
func newPropagationReactor(s *p2p.Switch, tr trace.Tracer, pv types.PrivValidator) *Reactor {
	blockStore := store.NewBlockStore(dbm.NewMemDB())
	pub, err := pv.GetPubKey()
	if err != nil {
		panic(err)
	}
	partsChan := make(chan types.PartInfo, 1000)
	proposalChan := make(chan types.Proposal, 100)
	blockPropR := NewReactor(s.NetAddress().ID, Config{
		Store:         blockStore,
		Mempool:       &mockMempool{txs: make(map[types.TxKey]*types.CachedTx)},
		Privval:       pv,
		ChainID:       TestChainID,
		BlockMaxBytes: 100000000,
		PartChan:      partsChan,
		ProposalChan:  proposalChan,
	})
	blockPropR.traceClient = tr
	blockPropR.currentProposer = pub
	// false means that we're not checking that the proposal was signed correctly by the proposer.
	// when creating a test proposal, we're currently not signing it
	blockPropR.started.Store(true)
	blockPropR.SetSwitch(s)

	return blockPropR
}

func testBlockPropReactors(n int, p2pCfg *cfg.P2PConfig) ([]*Reactor, []*p2p.Switch) {
	return createTestReactors(n, p2pCfg, false, "")
}

// createTestReactors will generate n propagation reactors, each using the same key to sign and verify compact blocks.
func createTestReactors(n int, p2pCfg *cfg.P2PConfig, tracer bool, traceDir string) ([]*Reactor, []*p2p.Switch) {
	reactors := make([]*Reactor, n)
	switches := make([]*p2p.Switch, n)

	p2p.MakeConnectedSwitches(p2pCfg, n, func(i int, s *p2p.Switch) *p2p.Switch {
		var (
			err error
			tr  = trace.NoOpTracer()
		)

		if tracer {
			dconfig := cfg.DefaultConfig()
			dconfig.SetRoot(filepath.Join(traceDir, strconv.Itoa(i)))
			tr, err = trace.NewLocalTracer(dconfig, log.NewNopLogger(), "test", string(s.NetAddress().ID))
			if err != nil {
				panic(err)
			}
		}

		reactors[i] = newPropagationReactor(s, tr, mockPrivVal)
		reactors[i].SetLogger(log.NewNopLogger())
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
				{
					Index: 0,
					Hash:  hashes[0],
				},
			},
		},
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
				{
					Index: 0,
					Hash:  hashes[0],
				},
			},
		},
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

// TestPropagationSmokeTest is a high level smoke test for 10 reactors to distribute 5 2MB
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
		prop, ps, block, metaData := createTestProposal(t, sm, i, 2, 1000000)

		// predistribute portions of the block
		for _, tx := range block.Txs {
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
			r.Prune(i)
		}
	}
}

func TestStopPeerForError(t *testing.T) {
	t.Run("invalid compact block: incorrect original part set hash", func(t *testing.T) {
		reactors, _ := testBlockPropReactors(2, cfg.DefaultP2PConfig())
		reactor1 := reactors[0]
		reactor2 := reactors[1]
		cb, _, _, _ := testCompactBlock(t, 10, 3)
		// put an invalid part set hash
		cb.Proposal.BlockID.PartSetHeader.Hash = cmtrand.Bytes(32)
		require.NotNil(t, reactor1.getPeer(reactor2.self))
		reactor1.handleCompactBlock(cb, reactor2.self, true)
		assert.Nil(t, reactor1.getPeer(reactor2.self))
	})
	t.Run("invalid message", func(t *testing.T) {
		reactors, _ := testBlockPropReactors(2, cfg.DefaultP2PConfig())
		reactor1 := reactors[0]
		reactor2 := reactors[1]
		reactor2Peer := reactor1.getPeer(reactor2.self).peer
		require.NotNil(t, reactor2Peer)
		invalidEnvelope := p2p.Envelope{
			Src:       reactor2Peer,
			Message:   &propagation.CompactBlock{BpHash: cmtrand.Bytes(2)},
			ChannelID: byte(0x05),
		}
		reactor1.ReceiveEnvelope(invalidEnvelope)
		assert.Nil(t, reactor1.getPeer(reactor2.self))
	})
	t.Run("invalid compact block: incorrect leaf hash", func(t *testing.T) {
		// TODO implement this test case
	})
	t.Run("have part for unknown proposal", func(t *testing.T) {
		t.Skip() // skipping for now
		reactors, _ := testBlockPropReactors(2, cfg.DefaultP2PConfig())
		reactor1 := reactors[0]
		reactor2 := reactors[1]
		require.NotNil(t, reactor1.getPeer(reactor2.self))
		// send haves before receiving a proposal
		reactor1.handleHaves(reactor2.self, &proptypes.HaveParts{
			Height: 1,
			Round:  2,
		})
		assert.Nil(t, reactor1.getPeer(reactor2.self))
	})
	t.Run("want part for unknown proposal", func(t *testing.T) {
		t.Skip() // skipping for now
		reactors, _ := testBlockPropReactors(2, cfg.DefaultP2PConfig())
		reactor1 := reactors[0]
		reactor2 := reactors[1]
		require.NotNil(t, reactor1.getPeer(reactor2.self))
		// send wants before receiving a proposal
		reactor1.handleWants(reactor2.self, &proptypes.WantParts{
			Height: 1,
			Round:  2,
		})
		assert.Nil(t, reactor1.getPeer(reactor2.self))
	})
	t.Run("recovery part for unknown proposal", func(t *testing.T) {
		t.Skip() // skipping for now
		reactors, _ := testBlockPropReactors(2, cfg.DefaultP2PConfig())
		reactor1 := reactors[0]
		reactor2 := reactors[1]
		require.NotNil(t, reactor1.getPeer(reactor2.self))
		// send wants before receiving a proposal
		reactor1.handleRecoveryPart(reactor2.self, &proptypes.RecoveryPart{
			Height: 1,
			Round:  2,
		})
		assert.Nil(t, reactor1.getPeer(reactor2.self))
	})
}

func TestConcurrentRequestLimit(t *testing.T) {
	tests := []struct {
		name          string
		peersCount    int
		partsCount    int
		expectedLimit int64
	}{
		{
			name:          "Zero peers and parts",
			peersCount:    0,
			partsCount:    0,
			expectedLimit: 1,
		},
		{
			name:          "One peer, no parts",
			peersCount:    1,
			partsCount:    0,
			expectedLimit: 1,
		},
		{
			name:          "Two peers, one part",
			peersCount:    2,
			partsCount:    1,
			expectedLimit: 1,
		},
		{
			name:          "Three peers, two parts",
			peersCount:    3,
			partsCount:    2,
			expectedLimit: 1,
		},
		{
			name:          "Four peers, many parts",
			peersCount:    4,
			partsCount:    10,
			expectedLimit: 3,
		},
		{
			name:          "Large peers and parts count",
			peersCount:    100,
			partsCount:    500,
			expectedLimit: 8,
		},
		{
			name:          "Odd division of parts by peers",
			peersCount:    7,
			partsCount:    20,
			expectedLimit: 4,
		},
		{
			name:          "Minimal redundancy case",
			peersCount:    3,
			partsCount:    1,
			expectedLimit: 1,
		},
		{
			name:          "Large redundancy case",
			peersCount:    100,
			partsCount:    100000,
			expectedLimit: 1516,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			limit := ConcurrentRequestLimit(tc.peersCount, tc.partsCount)
			if limit != tc.expectedLimit {
				t.Errorf("expected %d, got %d", tc.expectedLimit, limit)
			}
		})
	}
}

// testCompactBlock returns a test compact block with the corresponding orignal part set,
// parity partset, and proofs.
func testCompactBlock(t *testing.T, height int64, round int32) (*proptypes.CompactBlock, *types.PartSet, *types.PartSet, []*merkle.Proof) {
	ps := types.NewPartSetFromData(cmtrand.Bytes(1000), types.BlockPartSizeBytes)
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

	return baseCompactBlock, ps, pse, proofs
}

func createBitArray(size int, indices []int) *bits.BitArray {
	ba := bits.NewBitArray(size)
	for _, index := range indices {
		ba.SetIndex(index, true)
	}
	return ba
}

func NewTestPrivval(t *testing.T) types.PrivValidator {
	tempKeyFile, err := os.CreateTemp("", "priv_validator_key_")
	require.Nil(t, err)

	tempStateFile, err := os.CreateTemp("", "priv_validator_state_")
	require.Nil(t, err)

	privVal := privval.GenFilePV(tempKeyFile.Name(), tempStateFile.Name())
	return privVal
}
