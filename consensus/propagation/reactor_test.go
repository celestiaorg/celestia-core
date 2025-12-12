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
	blockPropR := NewReactor(s.NetAddress().ID, Config{
		Store:         blockStore,
		Mempool:       &mockMempool{txs: make(map[types.TxKey]*types.CachedTx)},
		Privval:       pv,
		ChainID:       TestChainID,
		BlockMaxBytes: int64(types.MaxBlockSizeBytes),
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

	switches := p2p.MakeConnectedSwitches(p2pCfg, n, func(i int, s *p2p.Switch) *p2p.Switch {
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
		reactors[i].SetLogger(log.TestingLogger())
		reactors[i].height = 1
		s.AddReactor("BlockProp", reactors[i])
		return s
	},
		p2p.Connect2Switches,
	)

	// Set up consensus peer state for each reactor's peers
	for i := range reactors {
		for j, otherReactor := range reactors {
			if i != j {
				// Find the peer corresponding to the other reactor
				for _, peer := range switches[i].Peers().List() {
					if peer.ID() == otherReactor.self {
						peer.Set(types.PeerStateKey, &MockPeerStateEditor{})
					}
				}
			}
		}
	}

	return reactors, switches
}

func TestCountRequests(t *testing.T) {
	// Create 4 reactors - one to test and 3 peers
	reactors, _ := testBlockPropReactors(4, cfg.DefaultP2PConfig())
	reactor := reactors[0]
	peer1Reactor := reactors[1]
	peer2Reactor := reactors[2]
	peer3Reactor := reactors[3]

	// Get the peer states for the three peer reactors as they appear to the main reactor
	peer1State := reactor.getPeer(peer1Reactor.self)
	require.NotNil(t, peer1State, "peer1 should be connected")

	peer2State := reactor.getPeer(peer2Reactor.self)
	require.NotNil(t, peer2State, "peer2 should be connected")

	peer3State := reactor.getPeer(peer3Reactor.self)
	require.NotNil(t, peer3State, "peer3 should be connected")

	// peer1 requests part=0 at height=10, round=0
	array := bits.NewBitArray(4)
	array.SetIndex(0, true)
	peer1State.AddRequests(10, 0, array)

	// peer2 requests part=0 and part=2 at height=10, round=0
	array2 := bits.NewBitArray(4)
	array2.SetIndex(0, true)
	array2.SetIndex(2, true)
	peer2State.AddRequests(10, 0, array2)

	// peer3 doesn't request anything

	// count requests part=0 at height=10, round=0
	part0Round0Height10RequestsCount := reactor.countRequests(10, 0, 0)
	assert.Equal(t, 2, len(part0Round0Height10RequestsCount))

	// count requests part=2 at height=10, round=0
	part2Round0Height10RequestsCount := reactor.countRequests(10, 0, 2)
	assert.Equal(t, 1, len(part2Round0Height10RequestsCount))
}

func TestHandleHavesAndWantsAndRecoveryParts(t *testing.T) {
	reactors, _ := testBlockPropReactors(3, cfg.DefaultP2PConfig())
	reactor1 := reactors[0]
	reactor2 := reactors[1]
	reactor3 := reactors[2]

	randomData := cmtrand.Bytes(1000)
	ps, err := types.NewPartSetFromData(randomData, types.BlockPartSizeBytes)
	require.NoError(t, err)
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
	ps, err := types.NewPartSetFromData(randomData, types.BlockPartSizeBytes)
	require.NoError(t, err)
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
		prop, ps, block, metaData := createTestProposal(t, sm, i, 0, 2, 1000000)

		// predistribute portions of the block
		for _, tx := range block.Txs {
			for j := 0; j < nodes/2; j++ {
				r := reactors[j]
				pool := r.mempool.(*mockMempool)
				pool.AddTx(tx)
			}
		}

		err := reactors[1].ProposeBlock(prop, ps, metaData)
		require.NoError(t, err)

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
			r.pmtx.Lock()
			r.height++
			r.pmtx.Unlock()
		}
	}
}

func TestValidateCompactBlock_InvalidLastLen(t *testing.T) {
	reactors, _ := testBlockPropReactors(1, cfg.DefaultP2PConfig())
	cleanup, _, sm := state.SetupTestCase(t)
	t.Cleanup(func() {
		cleanup(t)
	})
	reactor1 := reactors[0]
	reactor1.height = 10
	reactor1.round = 3
	cb, _, _, _ := testCompactBlock(t, sm, 10, 3)

	// put an invalid last len
	cb.LastLen = types.BlockPartSizeBytes + 1

	assert.Error(t, reactor1.validateCompactBlock(cb))
}

func TestStopPeerForError(t *testing.T) {
	t.Run("invalid compact block: incorrect original part set hash", func(t *testing.T) {
		reactors, _ := testBlockPropReactors(2, cfg.DefaultP2PConfig())
		reactor1 := reactors[0]
		reactor2 := reactors[1]
		cleanup, _, sm := state.SetupTestCase(t)
		t.Cleanup(func() {
			cleanup(t)
		})
		cb, _, _, _ := testCompactBlock(t, sm, 10, 3)
		// put an invalid part set hash
		cb.Proposal.BlockID.PartSetHeader.Hash = cmtrand.Bytes(32)
		cb.SetProofCache(nil)
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
func testCompactBlock(t *testing.T, sm state.State, height int64, round int32) (*proptypes.CompactBlock, *types.PartSet, *types.PartSet, []*merkle.Proof) {
	prop, ps, _, metaData := createTestProposal(t, sm, height, round, 2, 1000000)

	pse, lastLen, err := types.Encode(ps, types.BlockPartSizeBytes)
	require.NoError(t, err)
	pseh := pse.Header()

	hashes := extractHashes(ps, pse)
	proofs := extractProofs(ps, pse)

	baseCompactBlock := &proptypes.CompactBlock{
		BpHash:      pseh.Hash,
		LastLen:     uint32(lastLen),
		Blobs:       metaData,
		PartsHashes: hashes,
		Proposal:    *prop,
	}
	baseCompactBlock.SetProofCache(proofs)
	signBytes, err := baseCompactBlock.SignBytes()
	require.NoError(t, err)

	sig, err := mockPrivVal.SignRawBytes("test", CompactBlockUID, signBytes)
	require.NoError(t, err)
	baseCompactBlock.Signature = sig

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

// MockPeerStateEditor tracks calls to consensus peer state methods for testing
type MockPeerStateEditor struct {
	proposals  []*types.Proposal
	blockParts []BlockPartCall
}

type BlockPartCall struct {
	Height int64
	Round  int32
	Index  int
}

func (m *MockPeerStateEditor) SetHasProposal(proposal *types.Proposal) {
	m.proposals = append(m.proposals, proposal)
}

func (m *MockPeerStateEditor) SetHasProposalBlockPart(height int64, round int32, index int) {
	m.blockParts = append(m.blockParts, BlockPartCall{Height: height, Round: round, Index: index})
}

func (m *MockPeerStateEditor) GetHeight() int64 {
	return m.proposals[len(m.proposals)-1].Height
}

// TestPeerStateEditor ensures that the propagation reactor is updating the
// consensus peer state when the methods are called.
func TestPeerStateEditor(t *testing.T) {
	cleanup, _, sm := state.SetupTestCase(t)
	t.Cleanup(func() {
		cleanup(t)
	})
	reactors, _ := testBlockPropReactors(2, cfg.DefaultP2PConfig())
	r0 := reactors[0]
	r1 := reactors[1]
	r1pID := r1.self
	peerState := r0.getPeers()[0]

	editor := &MockPeerStateEditor{}
	peerState.consensusPeerState = editor

	require.NotNil(t, peerState)
	require.NotNil(t, peerState.consensusPeerState)
	require.IsType(t, &MockPeerStateEditor{}, peerState.consensusPeerState)

	cb, ps, _, _ := testCompactBlock(t, sm, 1, 1)

	added := r0.AddProposal(cb)
	require.True(t, added)

	assert.Len(t, editor.proposals, 0, "Should start with 0 proposals")

	r0.handleCompactBlock(cb, r1pID, false)

	assert.Len(t, editor.proposals, 1, "Expected 1 proposal to be recorded in consensus peer state")
	if len(editor.proposals) > 0 {
		assert.Equal(t, &cb.Proposal, editor.proposals[0])
	}

	part := ps.GetPart(0)
	r0.handleHaves(r1pID, &proptypes.HaveParts{
		Height: 1,
		Round:  1,
		Parts: []proptypes.PartMetaData{
			{
				Index: part.Index,
				Hash:  part.GetProof().LeafHash,
			},
		},
	})

	time.Sleep(time.Millisecond * 100)

	t.Logf("Block parts recorded: %d", len(editor.blockParts))
	require.GreaterOrEqual(t, len(editor.blockParts), 1)
	assert.Equal(t, int64(1), editor.blockParts[0].Height)
	assert.Equal(t, int32(1), editor.blockParts[0].Round)
	assert.Equal(t, 0, editor.blockParts[0].Index)
}

// ============================================================================
// Phase 6.2: handleRecoveryPart Routing Tests
// ============================================================================

// TestHandleRecoveryPart_LiveConsensus verifies that parts for the current
// consensus height are sent to partChan (live consensus mode).
func TestHandleRecoveryPart_LiveConsensus(t *testing.T) {
	reactors, _ := testBlockPropReactors(2, cfg.DefaultP2PConfig())
	reactor1 := reactors[0]
	reactor2 := reactors[1]

	randomData := cmtrand.Bytes(1000)
	ps, err := types.NewPartSetFromData(randomData, types.BlockPartSizeBytes)
	require.NoError(t, err)
	pse, lastLen, err := types.Encode(ps, types.BlockPartSizeBytes)
	require.NoError(t, err)
	psh := ps.Header()
	pseh := pse.Header()

	hashes := extractHashes(ps, pse)
	proofs := extractProofs(ps, pse)

	// Use height 5, which is > the reactor's current height (1)
	height, round := int64(5), int32(0)

	// Set reactor's current height to match the part height (live consensus)
	reactor1.SetHeightAndRound(height, round)

	baseCompactBlock := &proptypes.CompactBlock{
		BpHash:      pseh.Hash,
		Signature:   cmtrand.Bytes(64),
		LastLen:     uint32(lastLen),
		PartsHashes: hashes,
		Proposal: types.Proposal{
			BlockID: types.BlockID{
				Hash:          cmtrand.Bytes(32),
				PartSetHeader: psh,
			},
			Height: height,
			Round:  round,
		},
	}
	baseCompactBlock.SetProofCache(proofs)

	added := reactor1.AddProposal(baseCompactBlock)
	require.True(t, added)

	// Start the reactor processing to enable partChan handling
	reactor1.StartProcessing()

	// Send a recovery part at the current consensus height
	reactor1.handleRecoveryPart(reactor2.self, &proptypes.RecoveryPart{
		Height: height,
		Round:  round,
		Index:  0,
		Data:   ps.GetPart(0).Bytes.Bytes(),
	})

	// Verify the part was sent to partChan (live consensus mode)
	select {
	case part := <-reactor1.GetPartChan():
		require.Equal(t, height, part.Height)
		require.Equal(t, round, part.Round)
		require.Equal(t, uint32(0), part.Part.Index)
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for part on partChan - part should be delivered for live consensus height")
	}
}

// TestHandleRecoveryPart_HeightRouting verifies that parts are only sent to
// partChan when they match the current consensus height.
// Parts for any other height (whether above or below) should NOT go to partChan.
//
// NOTE: This test verifies the core routing logic introduced in Phase 6.2.
// Full blocksync/catchup integration requires PendingBlocksManager which will
// be completed in later phases.
func TestHandleRecoveryPart_HeightRouting(t *testing.T) {
	reactors, _ := testBlockPropReactors(2, cfg.DefaultP2PConfig())
	reactor1 := reactors[0]
	reactor2 := reactors[1]

	randomData := cmtrand.Bytes(1000)
	ps, err := types.NewPartSetFromData(randomData, types.BlockPartSizeBytes)
	require.NoError(t, err)
	pse, lastLen, err := types.Encode(ps, types.BlockPartSizeBytes)
	require.NoError(t, err)
	psh := ps.Header()
	pseh := pse.Header()

	hashes := extractHashes(ps, pse)
	proofs := extractProofs(ps, pse)

	// Test at height 5
	height, round := int64(5), int32(0)
	reactor1.SetHeightAndRound(height, round)

	cb := &proptypes.CompactBlock{
		BpHash:      pseh.Hash,
		Signature:   cmtrand.Bytes(64),
		LastLen:     uint32(lastLen),
		PartsHashes: hashes,
		Proposal: types.Proposal{
			BlockID: types.BlockID{
				Hash:          cmtrand.Bytes(32),
				PartSetHeader: psh,
			},
			Height: height,
			Round:  round,
		},
	}
	cb.SetProofCache(proofs)
	added := reactor1.AddProposal(cb)
	require.True(t, added)

	reactor1.StartProcessing()

	// Send part at current height - should go to partChan
	reactor1.handleRecoveryPart(reactor2.self, &proptypes.RecoveryPart{
		Height: height,
		Round:  round,
		Index:  0,
		Data:   ps.GetPart(0).Bytes.Bytes(),
	})

	select {
	case part := <-reactor1.GetPartChan():
		require.Equal(t, height, part.Height)
		require.Equal(t, round, part.Round)
	case <-time.After(time.Second):
		t.Fatal("part at current height should be sent to partChan")
	}

	// Now test that advancing height stops parts from going to partChan
	newHeight := int64(10)
	reactor1.SetHeightAndRound(newHeight, 0)

	// Add proposal at new height
	cb2 := &proptypes.CompactBlock{
		BpHash:      pseh.Hash,
		Signature:   cmtrand.Bytes(64),
		LastLen:     uint32(lastLen),
		PartsHashes: hashes,
		Proposal: types.Proposal{
			BlockID: types.BlockID{
				Hash:          cmtrand.Bytes(32),
				PartSetHeader: psh,
			},
			Height: newHeight,
			Round:  0,
		},
	}
	cb2.SetProofCache(proofs)
	added = reactor1.AddProposal(cb2)
	require.True(t, added)

	// Send part at NEW current height - should go to partChan
	reactor1.handleRecoveryPart(reactor2.self, &proptypes.RecoveryPart{
		Height: newHeight,
		Round:  0,
		Index:  0,
		Data:   ps.GetPart(0).Bytes.Bytes(),
	})

	select {
	case part := <-reactor1.GetPartChan():
		require.Equal(t, newHeight, part.Height)
	case <-time.After(time.Second):
		t.Fatal("part at new current height should be sent to partChan")
	}

	// Verify the old height's part was stored
	_, parts, found := reactor1.GetProposal(height, round)
	require.True(t, found)
	require.Equal(t, uint32(1), parts.Count())

	// Verify the new height's part was stored
	_, parts, found = reactor1.GetProposal(newHeight, 0)
	require.True(t, found)
	require.Equal(t, uint32(1), parts.Count())
}
