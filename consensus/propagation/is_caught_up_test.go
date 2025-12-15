package propagation

import (
	"testing"

	"github.com/cometbft/cometbft/libs/log"
	cmtrand "github.com/cometbft/cometbft/libs/rand"
	"github.com/cometbft/cometbft/p2p"
	cmttypes "github.com/cometbft/cometbft/types"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// Phase 8: IsCaughtUp Tests
// ============================================================================

// TestIsCaughtUp_LegacyMode verifies that IsCaughtUp returns false when
// pendingBlocks or hsReader is not configured (legacy mode).
func TestIsCaughtUp_LegacyMode(t *testing.T) {
	p2pCfg := defaultTestP2PConf()
	reactors, _ := createTestReactors(2, p2pCfg, false, "")

	n1 := reactors[0]

	// Without pendingBlocks configured, should return false
	n1.pendingBlocks = nil
	n1.hsReader = nil
	require.False(t, n1.IsCaughtUp(), "should return false when pendingBlocks is nil")

	// With pendingBlocks but no hsReader, should return false
	mgr := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{})
	n1.pendingBlocks = mgr
	n1.hsReader = nil
	require.False(t, n1.IsCaughtUp(), "should return false when hsReader is nil")
}

// TestIsCaughtUp_NoPeers verifies that IsCaughtUp returns false when no peers are connected.
func TestIsCaughtUp_NoPeers(t *testing.T) {
	// Create a single reactor without connecting to others
	p2pCfg := defaultTestP2PConf()
	reactors, _ := createTestReactors(1, p2pCfg, false, "")
	n1 := reactors[0]

	mgr := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{})
	mockHS := &mockHeaderSyncReaderForCaughtUp{caughtUp: true}

	n1.pendingBlocks = mgr
	n1.hsReader = mockHS

	// Clear peers
	n1.mtx.Lock()
	n1.peerstate = make(map[p2p.ID]*PeerState)
	n1.mtx.Unlock()

	require.False(t, n1.IsCaughtUp(), "should return false when no peers connected")
}

// TestIsCaughtUp_HeaderSyncNotCaughtUp verifies that IsCaughtUp returns false
// when headersync is not caught up.
func TestIsCaughtUp_HeaderSyncNotCaughtUp(t *testing.T) {
	p2pCfg := defaultTestP2PConf()
	reactors, _ := createTestReactors(2, p2pCfg, false, "")

	n1 := reactors[0]
	mgr := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{})
	mockHS := &mockHeaderSyncReaderForCaughtUp{caughtUp: false}

	n1.pendingBlocks = mgr
	n1.hsReader = mockHS
	n1.started.Store(true)

	require.False(t, n1.IsCaughtUp(), "should return false when headersync is not caught up")
}

// TestIsCaughtUp_PendingBlocksBelowConsensusHeight verifies that IsCaughtUp
// returns false when there are pending blocks below the consensus height.
func TestIsCaughtUp_PendingBlocksBelowConsensusHeight(t *testing.T) {
	p2pCfg := defaultTestP2PConf()
	reactors, _ := createTestReactors(2, p2pCfg, false, "")

	n1 := reactors[0]
	mgr := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{})
	mockHS := &mockHeaderSyncReaderForCaughtUp{caughtUp: true}

	n1.pendingBlocks = mgr
	n1.hsReader = mockHS
	n1.started.Store(true)

	// Set consensus height to 100
	n1.pmtx.Lock()
	n1.height = 100
	n1.pmtx.Unlock()

	// Add a pending block at height 50 (below consensus height)
	psh := cmttypes.PartSetHeader{
		Total: 5,
		Hash:  cmtrand.Bytes(32),
	}
	mgr.AddFromCommitment(50, 0, &psh)

	require.False(t, n1.IsCaughtUp(), "should return false when pending blocks below consensus height")
}

// TestIsCaughtUp_AllConditionsMet verifies that IsCaughtUp returns true
// when all conditions are met.
func TestIsCaughtUp_AllConditionsMet(t *testing.T) {
	p2pCfg := defaultTestP2PConf()
	reactors, _ := createTestReactors(2, p2pCfg, false, "")

	n1 := reactors[0]
	mgr := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{})
	mockHS := &mockHeaderSyncReaderForCaughtUp{caughtUp: true}

	n1.pendingBlocks = mgr
	n1.hsReader = mockHS
	n1.started.Store(true)

	// Set consensus height to 100
	n1.pmtx.Lock()
	n1.height = 100
	n1.pmtx.Unlock()

	// No pending blocks (empty manager) - this means we're caught up
	require.True(t, n1.IsCaughtUp(), "should return true when no pending blocks")
}

// TestIsCaughtUp_PendingBlocksAtConsensusHeight verifies that IsCaughtUp
// returns true when pending blocks are at or above consensus height.
func TestIsCaughtUp_PendingBlocksAtConsensusHeight(t *testing.T) {
	p2pCfg := defaultTestP2PConf()
	reactors, _ := createTestReactors(2, p2pCfg, false, "")

	n1 := reactors[0]
	mgr := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{})
	mockHS := &mockHeaderSyncReaderForCaughtUp{caughtUp: true}

	n1.pendingBlocks = mgr
	n1.hsReader = mockHS
	n1.started.Store(true)

	// Set consensus height to 100
	n1.pmtx.Lock()
	n1.height = 100
	n1.pmtx.Unlock()

	// Add a pending block at the current consensus height (this is live consensus)
	psh := cmttypes.PartSetHeader{
		Total: 5,
		Hash:  cmtrand.Bytes(32),
	}
	mgr.AddFromCommitment(100, 0, &psh)

	require.True(t, n1.IsCaughtUp(), "should return true when pending blocks at consensus height")
}

// TestIsCaughtUp_PendingBlocksAboveConsensusHeight verifies that IsCaughtUp
// returns true when pending blocks are above consensus height.
func TestIsCaughtUp_PendingBlocksAboveConsensusHeight(t *testing.T) {
	p2pCfg := defaultTestP2PConf()
	reactors, _ := createTestReactors(2, p2pCfg, false, "")

	n1 := reactors[0]
	mgr := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{})
	mockHS := &mockHeaderSyncReaderForCaughtUp{caughtUp: true}

	n1.pendingBlocks = mgr
	n1.hsReader = mockHS
	n1.started.Store(true)

	// Set consensus height to 100
	n1.pmtx.Lock()
	n1.height = 100
	n1.pmtx.Unlock()

	// Add a pending block above consensus height (future block)
	psh := cmttypes.PartSetHeader{
		Total: 5,
		Hash:  cmtrand.Bytes(32),
	}
	mgr.AddFromCommitment(105, 0, &psh)

	require.True(t, n1.IsCaughtUp(), "should return true when pending blocks above consensus height")
}

// ============================================================================
// WithHeaderSyncReader Option Tests
// ============================================================================

func TestWithHeaderSyncReader(t *testing.T) {
	mockHS := &mockHeaderSyncReaderForCaughtUp{caughtUp: true}

	p2pCfg := defaultTestP2PConf()
	reactors, _ := createTestReactors(1, p2pCfg, false, "")
	reactors[0].hsReader = mockHS

	require.Equal(t, mockHS, reactors[0].hsReader)
}

// mockHeaderSyncReaderForCaughtUp implements HeaderSyncReader for testing IsCaughtUp.
type mockHeaderSyncReaderForCaughtUp struct {
	caughtUp bool
}

func (m *mockHeaderSyncReaderForCaughtUp) GetVerifiedHeader(height int64) (*cmttypes.Header, *cmttypes.BlockID, bool) {
	return nil, nil, false
}

func (m *mockHeaderSyncReaderForCaughtUp) GetCommit(height int64) *cmttypes.Commit {
	return nil
}

func (m *mockHeaderSyncReaderForCaughtUp) Height() int64 {
	return 0
}

func (m *mockHeaderSyncReaderForCaughtUp) MaxPeerHeight() int64 {
	return 0
}

func (m *mockHeaderSyncReaderForCaughtUp) IsCaughtUp() bool {
	return m.caughtUp
}
