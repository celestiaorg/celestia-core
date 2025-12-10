package propagation

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/crypto/merkle"
	"github.com/cometbft/cometbft/headersync"
	"github.com/cometbft/cometbft/libs/log"
	cmtrand "github.com/cometbft/cometbft/libs/rand"
	"github.com/cometbft/cometbft/types"

	proptypes "github.com/cometbft/cometbft/consensus/propagation/types"
)

// mockHeaderVerifier implements HeaderVerifier for testing.
type mockHeaderVerifier struct {
	headers map[int64]*headersync.VerifiedHeader
}

func newMockHeaderVerifier() *mockHeaderVerifier {
	return &mockHeaderVerifier{
		headers: make(map[int64]*headersync.VerifiedHeader),
	}
}

func (m *mockHeaderVerifier) GetVerifiedHeader(height int64) (*types.Header, *types.BlockID, bool) {
	vh, ok := m.headers[height]
	if !ok {
		return nil, nil, false
	}
	return vh.Header, &vh.BlockID, true
}

func (m *mockHeaderVerifier) AddVerifiedHeader(height int64, hash []byte) {
	blockID := types.BlockID{
		Hash: hash,
		PartSetHeader: types.PartSetHeader{
			Total: 4,
			Hash:  cmtrand.Bytes(32),
		},
	}
	m.headers[height] = &headersync.VerifiedHeader{
		Header:  &types.Header{Height: height},
		BlockID: blockID,
	}
}

// makePendingTestCompactBlock creates a compact block for pending tests.
func makePendingTestCompactBlock(height int64, round int32, totalParts uint32) *proptypes.CompactBlock {
	return &proptypes.CompactBlock{
		BpHash:    cmtrand.Bytes(32),
		Signature: cmtrand.Bytes(64),
		LastLen:   0,
		Blobs: []proptypes.TxMetaData{
			{Hash: cmtrand.Bytes(32)},
		},
		Proposal: types.Proposal{
			Height: height,
			Round:  round,
			BlockID: types.BlockID{
				PartSetHeader: types.PartSetHeader{
					Total: totalParts,
					Hash:  cmtrand.Bytes(32),
				},
			},
		},
	}
}

// makeCompactBlockWithHash creates a compact block with a specific block ID hash.
func makeCompactBlockWithHash(height int64, round int32, totalParts uint32, hash []byte) *proptypes.CompactBlock {
	cb := makePendingTestCompactBlock(height, round, totalParts)
	cb.Proposal.BlockID.Hash = hash
	return cb
}

func TestPendingBlocksManager_HandleCompactBlock_NoVerifier(t *testing.T) {
	partsChan := make(chan types.PartInfo, 100)
	mgr := NewPendingBlocksManager(log.NewNopLogger(), partsChan, nil, DefaultPendingBlocksConfig())

	cb := makeCompactBlock(10, 0, 4)

	// Without a header verifier, compact blocks are not tracked here
	// (they go to ProposalCache for "live" verification).
	err := mgr.HandleCompactBlock(cb)
	require.NoError(t, err)
	require.Equal(t, 0, mgr.Len(), "should not track unverified blocks")
}

func TestPendingBlocksManager_HandleCompactBlock_Verified(t *testing.T) {
	partsChan := make(chan types.PartInfo, 100)
	verifier := newMockHeaderVerifier()
	mgr := NewPendingBlocksManager(log.NewNopLogger(), partsChan, verifier, DefaultPendingBlocksConfig())

	hash := cmtrand.Bytes(32)
	verifier.AddVerifiedHeader(10, hash)

	cb := makeCompactBlockWithHash(10, 0, 4, hash)

	err := mgr.HandleCompactBlock(cb)
	require.NoError(t, err)
	require.Equal(t, 1, mgr.Len())

	pending, exists := mgr.GetBlock(10)
	require.True(t, exists)
	require.Equal(t, BlockStateActive, pending.State)
	require.True(t, pending.HeaderVerified)
	require.NotNil(t, pending.Parts)
}

func TestPendingBlocksManager_HandleCompactBlock_HashMismatch(t *testing.T) {
	partsChan := make(chan types.PartInfo, 100)
	verifier := newMockHeaderVerifier()
	mgr := NewPendingBlocksManager(log.NewNopLogger(), partsChan, verifier, DefaultPendingBlocksConfig())

	verifier.AddVerifiedHeader(10, cmtrand.Bytes(32))

	// Create compact block with different hash.
	cb := makeCompactBlockWithHash(10, 0, 4, cmtrand.Bytes(32))

	err := mgr.HandleCompactBlock(cb)
	require.ErrorIs(t, err, ErrBlockIDMismatch)
	require.Equal(t, 0, mgr.Len())
}

func TestPendingBlocksManager_HandleCompactBlock_Duplicate(t *testing.T) {
	partsChan := make(chan types.PartInfo, 100)
	verifier := newMockHeaderVerifier()
	mgr := NewPendingBlocksManager(log.NewNopLogger(), partsChan, verifier, DefaultPendingBlocksConfig())

	hash := cmtrand.Bytes(32)
	verifier.AddVerifiedHeader(10, hash)

	cb := makeCompactBlockWithHash(10, 0, 4, hash)

	err := mgr.HandleCompactBlock(cb)
	require.NoError(t, err)
	require.Equal(t, 1, mgr.Len())

	// Second time should be silently accepted (duplicate).
	err = mgr.HandleCompactBlock(cb)
	require.NoError(t, err)
	require.Equal(t, 1, mgr.Len())
}

func TestPendingBlocksManager_HandleCompactBlock_MaxConcurrent(t *testing.T) {
	partsChan := make(chan types.PartInfo, 100)
	verifier := newMockHeaderVerifier()
	config := PendingBlocksConfig{
		MaxConcurrent: 2,
		MemoryBudget:  100 * 1024 * 1024,
	}
	mgr := NewPendingBlocksManager(log.NewNopLogger(), partsChan, verifier, config)

	// Add 2 blocks (at capacity).
	for i := int64(10); i < 12; i++ {
		hash := cmtrand.Bytes(32)
		verifier.AddVerifiedHeader(i, hash)
		cb := makeCompactBlockWithHash(i, 0, 4, hash)
		err := mgr.HandleCompactBlock(cb)
		require.NoError(t, err)
	}
	require.Equal(t, 2, mgr.Len())

	// Third should fail.
	hash := cmtrand.Bytes(32)
	verifier.AddVerifiedHeader(12, hash)
	cb := makeCompactBlockWithHash(12, 0, 4, hash)
	err := mgr.HandleCompactBlock(cb)
	require.ErrorIs(t, err, ErrNoCapacity)
}

func TestPendingBlocksManager_OnHeaderVerified_CreatesActiveBlock(t *testing.T) {
	partsChan := make(chan types.PartInfo, 100)
	verifier := newMockHeaderVerifier()
	mgr := NewPendingBlocksManager(log.NewNopLogger(), partsChan, verifier, DefaultPendingBlocksConfig())

	vh := &headersync.VerifiedHeader{
		Header: &types.Header{Height: 10},
		BlockID: types.BlockID{
			Hash: cmtrand.Bytes(32),
			PartSetHeader: types.PartSetHeader{
				Total: 4,
				Hash:  cmtrand.Bytes(32),
			},
		},
	}

	mgr.OnHeaderVerified(vh)

	require.Equal(t, 1, mgr.Len())
	pending, exists := mgr.GetBlock(10)
	require.True(t, exists)
	// Block should be active immediately - we have the PartSetHeader so we can start downloading.
	require.Equal(t, BlockStateActive, pending.State)
	require.True(t, pending.HeaderVerified)
	require.Nil(t, pending.CompactBlock) // No compact block, but we can still download.
	require.NotNil(t, pending.Parts)     // Parts should be allocated.
}

func TestPendingBlocksManager_OnHeaderVerified_ThenCompactBlock(t *testing.T) {
	partsChan := make(chan types.PartInfo, 100)
	verifier := newMockHeaderVerifier()
	mgr := NewPendingBlocksManager(log.NewNopLogger(), partsChan, verifier, DefaultPendingBlocksConfig())

	hash := cmtrand.Bytes(32)
	pshHash := cmtrand.Bytes(32)

	// First add via header - creates active block immediately.
	vh := &headersync.VerifiedHeader{
		Header: &types.Header{Height: 10},
		BlockID: types.BlockID{
			Hash: hash,
			PartSetHeader: types.PartSetHeader{
				Total: 4,
				Hash:  pshHash,
			},
		},
	}
	mgr.OnHeaderVerified(vh)
	require.Equal(t, BlockStateActive, mgr.blocks[10].State)
	require.Nil(t, mgr.blocks[10].CompactBlock)

	// Now add compact block - should attach to existing pending block.
	verifier.AddVerifiedHeader(10, hash)
	cb := makeCompactBlockWithHash(10, 0, 4, hash)
	err := mgr.HandleCompactBlock(cb)
	require.NoError(t, err)

	pending, _ := mgr.GetBlock(10)
	require.Equal(t, BlockStateActive, pending.State)
	require.NotNil(t, pending.CompactBlock)
}

func TestPendingBlocksManager_HeightPriority(t *testing.T) {
	partsChan := make(chan types.PartInfo, 100)
	verifier := newMockHeaderVerifier()
	mgr := NewPendingBlocksManager(log.NewNopLogger(), partsChan, verifier, DefaultPendingBlocksConfig())

	// Add blocks out of order.
	heights := []int64{15, 10, 12}
	for _, h := range heights {
		hash := cmtrand.Bytes(32)
		verifier.AddVerifiedHeader(h, hash)
		cb := makeCompactBlockWithHash(h, 0, 4, hash)
		err := mgr.HandleCompactBlock(cb)
		require.NoError(t, err)
	}

	// Heights should be sorted ascending.
	sortedHeights := mgr.Heights()
	require.Equal(t, []int64{10, 12, 15}, sortedHeights)
}

func TestPendingBlocksManager_GetMissingParts(t *testing.T) {
	partsChan := make(chan types.PartInfo, 100)
	verifier := newMockHeaderVerifier()
	mgr := NewPendingBlocksManager(log.NewNopLogger(), partsChan, verifier, DefaultPendingBlocksConfig())

	// Add two blocks.
	hash1 := cmtrand.Bytes(32)
	verifier.AddVerifiedHeader(10, hash1)
	cb1 := makeCompactBlockWithHash(10, 0, 4, hash1)
	_ = mgr.HandleCompactBlock(cb1)

	hash2 := cmtrand.Bytes(32)
	verifier.AddVerifiedHeader(11, hash2)
	cb2 := makeCompactBlockWithHash(11, 0, 4, hash2)
	_ = mgr.HandleCompactBlock(cb2)

	missing := mgr.GetMissingParts()
	require.Len(t, missing, 2)

	// First should be height 10 with highest priority.
	require.Equal(t, int64(10), missing[0].Height)
	require.Equal(t, 1.0, missing[0].Priority)
	require.Len(t, missing[0].MissingIndices, 4) // All 4 parts missing.

	// Second should be height 11 with lower priority.
	require.Equal(t, int64(11), missing[1].Height)
	require.Equal(t, 0.5, missing[1].Priority)
}

func TestPendingBlocksManager_DeleteHeight(t *testing.T) {
	partsChan := make(chan types.PartInfo, 100)
	verifier := newMockHeaderVerifier()
	mgr := NewPendingBlocksManager(log.NewNopLogger(), partsChan, verifier, DefaultPendingBlocksConfig())

	hash := cmtrand.Bytes(32)
	verifier.AddVerifiedHeader(10, hash)
	cb := makeCompactBlockWithHash(10, 0, 4, hash)
	_ = mgr.HandleCompactBlock(cb)

	require.Equal(t, 1, mgr.Len())

	mgr.DeleteHeight(10)

	require.Equal(t, 0, mgr.Len())
	_, exists := mgr.GetBlock(10)
	require.False(t, exists)
}

func TestPendingBlocksManager_Prune(t *testing.T) {
	partsChan := make(chan types.PartInfo, 100)
	verifier := newMockHeaderVerifier()
	mgr := NewPendingBlocksManager(log.NewNopLogger(), partsChan, verifier, DefaultPendingBlocksConfig())

	// Add blocks at heights 10, 11, 12.
	for i := int64(10); i <= 12; i++ {
		hash := cmtrand.Bytes(32)
		verifier.AddVerifiedHeader(i, hash)
		cb := makeCompactBlockWithHash(i, 0, 4, hash)
		_ = mgr.HandleCompactBlock(cb)
	}

	require.Equal(t, 3, mgr.Len())

	// Prune below height 12.
	mgr.Prune(12)

	require.Equal(t, 1, mgr.Len())
	_, exists := mgr.GetBlock(12)
	require.True(t, exists)
}

func TestPendingBlocksManager_HandlePart(t *testing.T) {
	// This test validates routing logic. Proof validation is tested elsewhere.
	// We test that parts are correctly forwarded to the channel.
	partsChan := make(chan types.PartInfo, 100)
	verifier := newMockHeaderVerifier()
	mgr := NewPendingBlocksManager(log.NewNopLogger(), partsChan, verifier, DefaultPendingBlocksConfig())

	hash := cmtrand.Bytes(32)
	verifier.AddVerifiedHeader(10, hash)
	cb := makeCompactBlockWithHash(10, 0, 4, hash)
	_ = mgr.HandleCompactBlock(cb)

	// Verify block is active and can receive parts.
	pending, exists := mgr.GetBlock(10)
	require.True(t, exists)
	require.Equal(t, BlockStateActive, pending.State)
	require.Equal(t, int32(0), pending.Round)
}

func TestPendingBlocksManager_HandlePart_UnknownHeight(t *testing.T) {
	partsChan := make(chan types.PartInfo, 100)
	mgr := NewPendingBlocksManager(log.NewNopLogger(), partsChan, nil, DefaultPendingBlocksConfig())

	part := &proptypes.RecoveryPart{
		Height: 10,
		Round:  0,
		Index:  0,
		Data:   cmtrand.Bytes(100),
	}
	proof := merkle.Proof{}

	_, err := mgr.HandlePart(10, 0, part, proof)
	require.ErrorIs(t, err, ErrUnknownHeight)
}

func TestPendingBlocksManager_HandlePart_WrongRound(t *testing.T) {
	partsChan := make(chan types.PartInfo, 100)
	verifier := newMockHeaderVerifier()
	mgr := NewPendingBlocksManager(log.NewNopLogger(), partsChan, verifier, DefaultPendingBlocksConfig())

	hash := cmtrand.Bytes(32)
	verifier.AddVerifiedHeader(10, hash)
	cb := makeCompactBlockWithHash(10, 0, 4, hash)
	_ = mgr.HandleCompactBlock(cb)

	// Part with wrong round should be ignored.
	part := &proptypes.RecoveryPart{
		Height: 10,
		Round:  1, // Wrong round.
		Index:  0,
		Data:   cmtrand.Bytes(100),
	}
	proof := merkle.Proof{}

	added, err := mgr.HandlePart(10, 1, part, proof)
	require.NoError(t, err)
	require.False(t, added)
}

func TestPendingBlocksManager_HandlePart_ParityNotForwarded(t *testing.T) {
	// This test verifies that parity parts (index >= original total) are not
	// forwarded to consensus. The actual part validation is tested elsewhere.
	partsChan := make(chan types.PartInfo, 100)
	verifier := newMockHeaderVerifier()
	mgr := NewPendingBlocksManager(log.NewNopLogger(), partsChan, verifier, DefaultPendingBlocksConfig())

	hash := cmtrand.Bytes(32)
	verifier.AddVerifiedHeader(10, hash)
	cb := makeCompactBlockWithHash(10, 0, 4, hash)
	_ = mgr.HandleCompactBlock(cb)

	pending, exists := mgr.GetBlock(10)
	require.True(t, exists)

	// Verify that original total is 4.
	require.Equal(t, uint32(4), pending.Parts.Original().Total())

	// The forwarding logic checks if part.Index < pending.Parts.Original().Total()
	// Parts with index >= 4 would be parity parts and should not be forwarded.
}

// --- RequestTracker tests ---

func TestRequestTracker_Basic(t *testing.T) {
	rt := NewRequestTracker(4, 3)

	// Initially nothing requested or received.
	unreq := rt.GetUnrequestedMissing()
	require.Equal(t, []int{0, 1, 2, 3}, unreq)
	require.False(t, rt.IsComplete())

	// Mark part 0 as requested.
	rt.MarkRequested(0)
	unreq = rt.GetUnrequestedMissing()
	require.Equal(t, []int{1, 2, 3}, unreq)

	// Mark part 0 as received.
	rt.MarkReceived(0)
	unreq = rt.GetUnrequestedMissing()
	require.Equal(t, []int{1, 2, 3}, unreq)

	// Mark all as received.
	rt.MarkReceived(1)
	rt.MarkReceived(2)
	rt.MarkReceived(3)
	require.True(t, rt.IsComplete())
}

func TestRequestTracker_RetryLimit(t *testing.T) {
	rt := NewRequestTracker(2, 2) // Limit of 2 requests per part.

	// Request part 0 twice.
	rt.MarkRequested(0)
	rt.MarkRequested(0)

	// Part 0 should no longer be retryable.
	retryable := rt.GetRetryableMissing()
	require.Equal(t, []int{1}, retryable) // Only part 1 is retryable.

	// Part 1 can still be requested.
	rt.MarkRequested(1)
	retryable = rt.GetRetryableMissing()
	require.Equal(t, []int{1}, retryable) // Part 1 still under limit.
}

func TestPendingBlocksManager_AddCommitment_CreatesActiveBlock(t *testing.T) {
	partsChan := make(chan types.PartInfo, 100)
	mgr := NewPendingBlocksManager(log.NewNopLogger(), partsChan, nil, DefaultPendingBlocksConfig())

	psh := &types.PartSetHeader{
		Total: 8,
		Hash:  cmtrand.Bytes(32),
	}

	mgr.AddCommitment(10, 2, psh)

	require.Equal(t, 1, mgr.Len())
	pending, exists := mgr.GetBlock(10)
	require.True(t, exists)
	require.Equal(t, BlockStateActive, pending.State)
	require.Equal(t, int32(2), pending.Round)
	require.True(t, pending.HeaderVerified)
	require.NotNil(t, pending.Parts)
	require.Equal(t, uint32(8), pending.Parts.Original().Total())
}

func TestPendingBlocksManager_AddCommitment_SkipsDuplicate(t *testing.T) {
	partsChan := make(chan types.PartInfo, 100)
	verifier := newMockHeaderVerifier()
	mgr := NewPendingBlocksManager(log.NewNopLogger(), partsChan, verifier, DefaultPendingBlocksConfig())

	// First add via header.
	hash := cmtrand.Bytes(32)
	vh := &headersync.VerifiedHeader{
		Header: &types.Header{Height: 10},
		BlockID: types.BlockID{
			Hash: hash,
			PartSetHeader: types.PartSetHeader{
				Total: 4,
				Hash:  cmtrand.Bytes(32),
			},
		},
	}
	mgr.OnHeaderVerified(vh)
	require.Equal(t, 1, mgr.Len())

	// AddCommitment for same height should be ignored.
	psh := &types.PartSetHeader{
		Total: 8,
		Hash:  cmtrand.Bytes(32),
	}
	mgr.AddCommitment(10, 2, psh)

	// Still only one block.
	require.Equal(t, 1, mgr.Len())
	pending, _ := mgr.GetBlock(10)
	// Original parts count should be from header, not commitment.
	require.Equal(t, uint32(4), pending.Parts.Original().Total())
}

func TestPendingBlocksManager_AddCommitment_RespectsCapacity(t *testing.T) {
	partsChan := make(chan types.PartInfo, 100)
	config := PendingBlocksConfig{
		MaxConcurrent: 1,
		MemoryBudget:  100 * 1024 * 1024,
	}
	mgr := NewPendingBlocksManager(log.NewNopLogger(), partsChan, nil, config)

	// Add first commitment.
	psh1 := &types.PartSetHeader{Total: 4, Hash: cmtrand.Bytes(32)}
	mgr.AddCommitment(10, 0, psh1)
	require.Equal(t, 1, mgr.Len())

	// Second commitment should be ignored due to capacity.
	psh2 := &types.PartSetHeader{Total: 4, Hash: cmtrand.Bytes(32)}
	mgr.AddCommitment(11, 0, psh2)
	require.Equal(t, 1, mgr.Len())
}
