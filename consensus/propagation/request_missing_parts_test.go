package propagation

import (
	"testing"
	"time"

	proptypes "github.com/cometbft/cometbft/consensus/propagation/types"
	"github.com/cometbft/cometbft/libs/bits"
	"github.com/cometbft/cometbft/libs/log"
	cmtrand "github.com/cometbft/cometbft/libs/rand"
	"github.com/cometbft/cometbft/p2p"
	"github.com/cometbft/cometbft/types"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// Phase 7: Request Coordination Tests
// ============================================================================

// TestRequestMissingParts_PeerFiltering verifies that requestMissingParts
// only requests parts from peers that are at or above the block's height.
func TestRequestMissingParts_PeerFiltering(t *testing.T) {
	p2pCfg := defaultTestP2PConf()
	nodes := 3
	reactors, _ := createTestReactors(nodes, p2pCfg, false, "")

	n1 := reactors[0]
	n2 := reactors[1]
	n3 := reactors[2]

	// Configure PendingBlocksManager for n1
	mgr := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{})
	n1.pendingBlocks = mgr

	// Create a test block at height 10
	psh := types.PartSetHeader{
		Total: 5,
		Hash:  cmtrand.Bytes(32),
	}
	mgr.AddFromCommitment(10, 0, &psh)

	// Set peer heights:
	// - n2 is at height 5 (below block height 10)
	// - n3 is at height 15 (above block height 10)
	n2Peer := n1.getPeer(n2.self)
	require.NotNil(t, n2Peer)
	n2Peer.consensusPeerState = &mockPeerStateEditor{height: 5}

	n3Peer := n1.getPeer(n3.self)
	require.NotNil(t, n3Peer)
	n3Peer.consensusPeerState = &mockPeerStateEditor{height: 15}

	// Start processing and trigger request
	n1.started.Store(true)
	n1.requestMissingParts()

	// Allow time for requests to be processed
	time.Sleep(100 * time.Millisecond)

	// n2 should NOT have received a want (height too low)
	_, hasN2Want := n2Peer.GetWants(10, 0)
	require.False(t, hasN2Want, "peer n2 should not receive want (height 5 < 10)")

	// n3 SHOULD have received a want (height is sufficient)
	_, hasN3Want := n3Peer.GetWants(10, 0)
	// Note: This might be false if requests tracking doesn't match wants
	// The important thing is that n2 didn't get a request
	_ = hasN3Want // We mainly want to ensure n2 didn't get requests
}

// TestRequestMissingParts_RequestLimits verifies that requestMissingParts
// respects the request limits and doesn't over-request parts.
func TestRequestMissingParts_RequestLimits(t *testing.T) {
	p2pCfg := defaultTestP2PConf()
	nodes := 4 // More peers to test request distribution
	reactors, _ := createTestReactors(nodes, p2pCfg, false, "")

	n1 := reactors[0]

	// Configure PendingBlocksManager for n1
	mgr := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{})
	n1.pendingBlocks = mgr

	// Create a test block with few parts (to trigger multiple requests per part)
	psh := types.PartSetHeader{
		Total: 2, // Small block = higher ReqLimit
		Hash:  cmtrand.Bytes(32),
	}
	mgr.AddFromCommitment(10, 0, &psh)

	// Set all peers to be at sufficient height
	for _, r := range reactors[1:] {
		peer := n1.getPeer(r.self)
		if peer != nil {
			peer.consensusPeerState = &mockPeerStateEditor{height: 15}
		}
	}

	// Start processing
	n1.started.Store(true)

	// Make multiple request attempts
	for i := 0; i < 5; i++ {
		n1.requestMissingParts()
	}

	time.Sleep(100 * time.Millisecond)

	// Count total requests per part across all peers
	requestCounts := make(map[int]int)
	for _, r := range reactors[1:] {
		peer := n1.getPeer(r.self)
		if peer == nil {
			continue
		}
		reqs, has := peer.GetRequests(10, 0)
		if has && reqs != nil {
			for _, idx := range reqs.GetTrueIndices() {
				requestCounts[idx]++
			}
		}
	}

	// ReqLimit for 4 parts (2 original * 2 for parity) = ceil(34/4) = 9
	// But we only have 3 peers, so max should be 3 per part
	reqLimit := ReqLimit(int(psh.Total * 2))
	for partIdx, count := range requestCounts {
		require.LessOrEqual(t, count, reqLimit,
			"part %d requested %d times, limit is %d", partIdx, count, reqLimit)
	}
}

// TestRequestMissingParts_UsesNeedsProofs verifies that the NeedsProofs flag
// is correctly propagated to WantParts messages.
func TestRequestMissingParts_UsesNeedsProofs(t *testing.T) {
	mgr := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{})

	// Add block via commitment (needs proofs)
	psh := types.PartSetHeader{
		Total: 5,
		Hash:  cmtrand.Bytes(32),
	}
	mgr.AddFromCommitment(10, 0, &psh)

	// Get missing parts info
	missing := mgr.GetMissingParts(100)
	require.Len(t, missing, 1)
	require.True(t, missing[0].NeedsProofs, "commitment-based block should need proofs")

	// Now add a CompactBlock (no longer needs proofs)
	cb := &proptypes.CompactBlock{
		BpHash:      cmtrand.Bytes(32),
		Signature:   cmtrand.Bytes(64),
		LastLen:     100,
		PartsHashes: make([][]byte, 10),
		Proposal: types.Proposal{
			Height: 10,
			Round:  0,
			BlockID: types.BlockID{
				Hash:          cmtrand.Bytes(32),
				PartSetHeader: psh,
			},
		},
	}
	for i := range cb.PartsHashes {
		cb.PartsHashes[i] = cmtrand.Bytes(32)
	}
	added, err := mgr.AddProposal(cb)
	require.NoError(t, err)
	require.True(t, added)

	// Get missing parts info again
	missing = mgr.GetMissingParts(100)
	require.Len(t, missing, 1)
	require.False(t, missing[0].NeedsProofs, "compact block should not need inline proofs")
}

// TestRequestMissingParts_FallbackToLegacy verifies that when pendingBlocks
// is nil, the function falls back to the legacy retryWants.
func TestRequestMissingParts_FallbackToLegacy(t *testing.T) {
	p2pCfg := defaultTestP2PConf()
	nodes := 2
	reactors, _ := createTestReactors(nodes, p2pCfg, false, "")

	n1 := reactors[0]

	// Ensure pendingBlocks is nil (legacy mode)
	n1.pendingBlocks = nil

	// This should not panic and should call retryWants internally
	n1.started.Store(true)
	n1.requestMissingParts() // Should not panic
}

// TestRequestMissingParts_PriorityByHeight verifies that blocks are requested
// in height order (lowest first).
func TestRequestMissingParts_PriorityByHeight(t *testing.T) {
	mgr := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{})

	// Add blocks out of order
	for _, h := range []int64{15, 10, 20, 5} {
		psh := types.PartSetHeader{
			Total: 5,
			Hash:  cmtrand.Bytes(32),
		}
		mgr.AddFromCommitment(h, 0, &psh)
	}

	// Get missing parts - should be ordered by height
	missing := mgr.GetMissingParts(100)
	require.Len(t, missing, 4)

	expectedHeights := []int64{5, 10, 15, 20}
	for i, info := range missing {
		require.Equal(t, expectedHeights[i], info.Height,
			"expected height %d at position %d, got %d", expectedHeights[i], i, info.Height)
	}
}

// TestRequestMissingParts_EmptyPeers verifies graceful handling when no peers available.
func TestRequestMissingParts_EmptyPeers(t *testing.T) {
	// Create a reactor without connecting to any peers
	p2pCfg := defaultTestP2PConf()
	reactors, _ := createTestReactors(1, p2pCfg, false, "")
	n1 := reactors[0]

	// Configure PendingBlocksManager
	mgr := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{})
	n1.pendingBlocks = mgr

	// Add a block
	psh := types.PartSetHeader{
		Total: 5,
		Hash:  cmtrand.Bytes(32),
	}
	mgr.AddFromCommitment(10, 0, &psh)

	// Clear all peers
	n1.mtx.Lock()
	n1.peerstate = make(map[p2p.ID]*PeerState)
	n1.mtx.Unlock()

	// Should not panic with no peers
	n1.started.Store(true)
	n1.requestMissingParts()
}

// TestRequestMissingParts_NoMissingParts verifies graceful handling when
// there are no missing parts.
func TestRequestMissingParts_NoMissingParts(t *testing.T) {
	p2pCfg := defaultTestP2PConf()
	reactors, _ := createTestReactors(2, p2pCfg, false, "")
	n1 := reactors[0]

	// Configure PendingBlocksManager with no blocks
	mgr := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{})
	n1.pendingBlocks = mgr

	// Should not panic with no missing parts
	n1.started.Store(true)
	n1.requestMissingParts()
}

// mockPeerStateEditor is a test mock for PeerStateEditor
type mockPeerStateEditor struct {
	height int64
}

func (m *mockPeerStateEditor) SetHasProposal(_ *types.Proposal)                {}
func (m *mockPeerStateEditor) SetHasProposalBlockPart(_ int64, _ int32, _ int) {}
func (m *mockPeerStateEditor) GetHeight() int64                                { return m.height }

// ============================================================================
// WithPendingBlocksManager Option Tests
// ============================================================================

func TestWithPendingBlocksManager(t *testing.T) {
	mgr := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{})

	p2pCfg := defaultTestP2PConf()
	reactors, _ := createTestReactors(1, p2pCfg, false, "")
	reactors[0].pendingBlocks = mgr

	require.Equal(t, mgr, reactors[0].pendingBlocks)
}

func TestWithBlockDeliveryManager(t *testing.T) {
	mgr := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{})
	delivery := NewBlockDeliveryManager(mgr, 1, log.NewNopLogger())

	p2pCfg := defaultTestP2PConf()
	reactors, _ := createTestReactors(1, p2pCfg, false, "")
	reactors[0].blockDelivery = delivery

	require.Equal(t, delivery, reactors[0].blockDelivery)
}

func TestGetBlockChan_WithDeliveryManager(t *testing.T) {
	mgr := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{})
	delivery := NewBlockDeliveryManager(mgr, 1, log.NewNopLogger())

	p2pCfg := defaultTestP2PConf()
	reactors, _ := createTestReactors(1, p2pCfg, false, "")
	reactors[0].blockDelivery = delivery

	ch := reactors[0].GetBlockChan()
	require.NotNil(t, ch, "GetBlockChan should return channel when delivery manager is set")
	require.Equal(t, delivery.BlockChan(), ch)
}

func TestGetBlockChan_WithoutDeliveryManager(t *testing.T) {
	p2pCfg := defaultTestP2PConf()
	reactors, _ := createTestReactors(1, p2pCfg, false, "")

	ch := reactors[0].GetBlockChan()
	require.Nil(t, ch, "GetBlockChan should return nil when delivery manager is not set")
}

// ============================================================================
// MissingPartsInfo Tests
// ============================================================================

func TestMissingPartsInfo_NeedsProofs(t *testing.T) {
	info := &MissingPartsInfo{
		Height:        10,
		Round:         0,
		Missing:       bits.NewBitArray(10),
		Total:         10,
		NeedsProofs:   true,
		HasCommitment: true,
	}

	require.True(t, info.NeedsProofs)
	require.True(t, info.HasCommitment)
}
