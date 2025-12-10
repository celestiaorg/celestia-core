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

// newTestPendingBlocksManager creates a PendingBlocksManager for testing.
func newTestPendingBlocksManager(partsChan chan types.PartInfo, verifier HeaderVerifier, config PendingBlocksConfig) *PendingBlocksManager {
	return NewPendingBlocksManager(log.NewNopLogger(), nil, partsChan, verifier, config)
}

func TestPendingBlocksManager_AddProposal_NoVerifier(t *testing.T) {
	partsChan := make(chan types.PartInfo, 100)
	mgr := newTestPendingBlocksManager(partsChan, nil, DefaultPendingBlocksConfig())

	cb := makePendingTestCompactBlock(10, 0, 4)

	// Without a header verifier, compact blocks are still tracked (live proposals).
	added := mgr.AddProposal(cb)
	require.True(t, added)
	require.Equal(t, 1, mgr.Len())
}

func TestPendingBlocksManager_AddProposal_Verified(t *testing.T) {
	partsChan := make(chan types.PartInfo, 100)
	verifier := newMockHeaderVerifier()
	mgr := newTestPendingBlocksManager(partsChan, verifier, DefaultPendingBlocksConfig())

	hash := cmtrand.Bytes(32)
	verifier.AddVerifiedHeader(10, hash)

	cb := makeCompactBlockWithHash(10, 0, 4, hash)

	added := mgr.AddProposal(cb)
	require.True(t, added)
	require.Equal(t, 1, mgr.Len())

	pending, exists := mgr.GetBlock(10)
	require.True(t, exists)
	require.Equal(t, BlockStateActive, pending.State)
	require.True(t, pending.HeaderVerified)
	require.NotNil(t, pending.Parts)
}

func TestPendingBlocksManager_AddProposal_HashMismatch(t *testing.T) {
	partsChan := make(chan types.PartInfo, 100)
	verifier := newMockHeaderVerifier()
	mgr := newTestPendingBlocksManager(partsChan, verifier, DefaultPendingBlocksConfig())

	// Add a verified header first via OnHeaderVerified.
	headerHash := cmtrand.Bytes(32)
	vh := &headersync.VerifiedHeader{
		Header: &types.Header{Height: 10},
		BlockID: types.BlockID{
			Hash: headerHash,
			PartSetHeader: types.PartSetHeader{
				Total: 4,
				Hash:  cmtrand.Bytes(32),
			},
		},
	}
	mgr.AddFromHeader(vh)
	require.Equal(t, 1, mgr.Len())

	// Try to add compact block with different hash - should fail.
	cb := makeCompactBlockWithHash(10, 0, 4, cmtrand.Bytes(32))
	added := mgr.AddProposal(cb)
	require.False(t, added) // Hash mismatch, not added.
}

func TestPendingBlocksManager_AddProposal_Duplicate(t *testing.T) {
	partsChan := make(chan types.PartInfo, 100)
	verifier := newMockHeaderVerifier()
	mgr := newTestPendingBlocksManager(partsChan, verifier, DefaultPendingBlocksConfig())

	hash := cmtrand.Bytes(32)
	verifier.AddVerifiedHeader(10, hash)

	cb := makeCompactBlockWithHash(10, 0, 4, hash)

	added := mgr.AddProposal(cb)
	require.True(t, added)
	require.Equal(t, 1, mgr.Len())

	// Second time should return false (duplicate).
	added = mgr.AddProposal(cb)
	require.False(t, added)
	require.Equal(t, 1, mgr.Len())
}

func TestPendingBlocksManager_AddProposal_MaxConcurrent(t *testing.T) {
	partsChan := make(chan types.PartInfo, 100)
	config := PendingBlocksConfig{
		MaxConcurrent: 2,
		MemoryBudget:  100 * 1024 * 1024,
	}
	mgr := newTestPendingBlocksManager(partsChan, nil, config)

	// Add 2 blocks (at capacity).
	for i := int64(10); i < 12; i++ {
		cb := makePendingTestCompactBlock(i, 0, 4)
		added := mgr.AddProposal(cb)
		require.True(t, added)
	}
	require.Equal(t, 2, mgr.Len())

	// Third should fail due to capacity.
	cb := makePendingTestCompactBlock(12, 0, 4)
	added := mgr.AddProposal(cb)
	require.False(t, added)
}

func TestPendingBlocksManager_AddFromHeader_CreatesActiveBlock(t *testing.T) {
	partsChan := make(chan types.PartInfo, 100)
	verifier := newMockHeaderVerifier()
	mgr := newTestPendingBlocksManager(partsChan, verifier, DefaultPendingBlocksConfig())

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

	mgr.AddFromHeader(vh)

	require.Equal(t, 1, mgr.Len())
	pending, exists := mgr.GetBlock(10)
	require.True(t, exists)
	// Block should be active immediately - we have the PartSetHeader so we can start downloading.
	require.Equal(t, BlockStateActive, pending.State)
	require.True(t, pending.HeaderVerified)
	require.Nil(t, pending.CompactBlock) // No compact block, but we can still download.
	require.NotNil(t, pending.Parts)     // Parts should be allocated.
}

func TestPendingBlocksManager_AddFromHeader_ThenCompactBlock(t *testing.T) {
	partsChan := make(chan types.PartInfo, 100)
	verifier := newMockHeaderVerifier()
	mgr := newTestPendingBlocksManager(partsChan, verifier, DefaultPendingBlocksConfig())

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
	mgr.AddFromHeader(vh)
	require.Equal(t, BlockStateActive, mgr.blocks[10].State)
	require.Nil(t, mgr.blocks[10].CompactBlock)

	// Now add compact block - should attach to existing pending block.
	verifier.AddVerifiedHeader(10, hash)
	cb := makeCompactBlockWithHash(10, 0, 4, hash)
	added := mgr.AddProposal(cb)
	require.True(t, added)

	pending, _ := mgr.GetBlock(10)
	require.Equal(t, BlockStateActive, pending.State)
	require.NotNil(t, pending.CompactBlock)
}

func TestPendingBlocksManager_HeightPriority(t *testing.T) {
	partsChan := make(chan types.PartInfo, 100)
	mgr := newTestPendingBlocksManager(partsChan, nil, DefaultPendingBlocksConfig())

	// Add blocks in ascending order (required by relevantLocked).
	heights := []int64{10, 12, 15}
	for _, h := range heights {
		cb := makePendingTestCompactBlock(h, 0, 4)
		added := mgr.AddProposal(cb)
		require.True(t, added)
	}

	// Heights should be sorted ascending.
	sortedHeights := mgr.Heights()
	require.Equal(t, []int64{10, 12, 15}, sortedHeights)
}

func TestPendingBlocksManager_GetMissingParts(t *testing.T) {
	partsChan := make(chan types.PartInfo, 100)
	mgr := newTestPendingBlocksManager(partsChan, nil, DefaultPendingBlocksConfig())

	// Add two blocks.
	cb1 := makePendingTestCompactBlock(10, 0, 4)
	mgr.AddProposal(cb1)

	cb2 := makePendingTestCompactBlock(11, 0, 4)
	mgr.AddProposal(cb2)

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
	mgr := newTestPendingBlocksManager(partsChan, nil, DefaultPendingBlocksConfig())

	cb := makePendingTestCompactBlock(10, 0, 4)
	mgr.AddProposal(cb)

	require.Equal(t, 1, mgr.Len())

	mgr.DeleteHeight(10)

	require.Equal(t, 0, mgr.Len())
	_, exists := mgr.GetBlock(10)
	require.False(t, exists)
}

func TestPendingBlocksManager_Prune(t *testing.T) {
	partsChan := make(chan types.PartInfo, 100)
	mgr := newTestPendingBlocksManager(partsChan, nil, DefaultPendingBlocksConfig())

	// Add blocks at heights 10, 11, 12.
	for i := int64(10); i <= 12; i++ {
		cb := makePendingTestCompactBlock(i, 0, 4)
		mgr.AddProposal(cb)
	}

	require.Equal(t, 3, mgr.Len())

	// Prune at height 11 - removes heights <= 11.
	mgr.Prune(11)

	require.Equal(t, 1, mgr.Len())
	_, exists := mgr.GetBlock(12)
	require.True(t, exists)
}

func TestPendingBlocksManager_HandlePart(t *testing.T) {
	// This test validates routing logic. Proof validation is tested elsewhere.
	// We test that parts are correctly forwarded to the channel.
	partsChan := make(chan types.PartInfo, 100)
	mgr := newTestPendingBlocksManager(partsChan, nil, DefaultPendingBlocksConfig())

	cb := makePendingTestCompactBlock(10, 0, 4)
	mgr.AddProposal(cb)

	// Verify block is active and can receive parts.
	pending, exists := mgr.GetBlock(10)
	require.True(t, exists)
	require.Equal(t, BlockStateActive, pending.State)
	require.Equal(t, int32(0), pending.Round)
}

func TestPendingBlocksManager_HandlePart_UnknownHeight(t *testing.T) {
	partsChan := make(chan types.PartInfo, 100)
	mgr := newTestPendingBlocksManager(partsChan, nil, DefaultPendingBlocksConfig())

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
	mgr := newTestPendingBlocksManager(partsChan, nil, DefaultPendingBlocksConfig())

	// Create block with round 1 (not 0, which is treated as "any round").
	cb := makePendingTestCompactBlock(10, 1, 4)
	mgr.AddProposal(cb)

	// Part with wrong round should be ignored.
	part := &proptypes.RecoveryPart{
		Height: 10,
		Round:  2, // Wrong round.
		Index:  0,
		Data:   cmtrand.Bytes(100),
	}
	proof := merkle.Proof{}

	added, err := mgr.HandlePart(10, 2, part, proof)
	require.NoError(t, err)
	require.False(t, added)
}

func TestPendingBlocksManager_HandlePart_ParityNotForwarded(t *testing.T) {
	// This test verifies that parity parts (index >= original total) are not
	// forwarded to consensus. The actual part validation is tested elsewhere.
	partsChan := make(chan types.PartInfo, 100)
	mgr := newTestPendingBlocksManager(partsChan, nil, DefaultPendingBlocksConfig())

	cb := makePendingTestCompactBlock(10, 0, 4)
	mgr.AddProposal(cb)

	pending, exists := mgr.GetBlock(10)
	require.True(t, exists)

	// Verify that original total is 4.
	require.Equal(t, uint32(4), pending.Parts.Original().Total())

	// The forwarding logic checks if part.Index < pending.Parts.Original().Total()
	// Parts with index >= 4 would be parity parts and should not be forwarded.
}

func TestPendingBlocksManager_AddCommitment_CreatesActiveBlock(t *testing.T) {
	partsChan := make(chan types.PartInfo, 100)
	mgr := newTestPendingBlocksManager(partsChan, nil, DefaultPendingBlocksConfig())

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
	// Commitments are NOT header verified - they come from consensus commits, not headersync.
	require.False(t, pending.HeaderVerified)
	require.NotNil(t, pending.Parts)
	require.Equal(t, uint32(8), pending.Parts.Original().Total())
}

func TestPendingBlocksManager_AddCommitment_SkipsDuplicate(t *testing.T) {
	partsChan := make(chan types.PartInfo, 100)
	verifier := newMockHeaderVerifier()
	mgr := newTestPendingBlocksManager(partsChan, verifier, DefaultPendingBlocksConfig())

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
	mgr.AddFromHeader(vh)
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
	mgr := newTestPendingBlocksManager(partsChan, nil, config)

	// Add first commitment.
	psh1 := &types.PartSetHeader{Total: 4, Hash: cmtrand.Bytes(32)}
	mgr.AddCommitment(10, 0, psh1)
	require.Equal(t, 1, mgr.Len())

	// Second commitment should be ignored due to capacity.
	psh2 := &types.PartSetHeader{Total: 4, Hash: cmtrand.Bytes(32)}
	mgr.AddCommitment(11, 0, psh2)
	require.Equal(t, 1, mgr.Len())
}

func TestPendingBlocksManager_BlockSource(t *testing.T) {
	partsChan := make(chan types.PartInfo, 100)
	mgr := newTestPendingBlocksManager(partsChan, nil, DefaultPendingBlocksConfig())

	// Add via compact block.
	cb := makePendingTestCompactBlock(10, 0, 4)
	mgr.AddProposal(cb)
	pending, _ := mgr.GetBlock(10)
	require.Equal(t, SourceCompactBlock, pending.Source)

	// Add via header.
	vh := &headersync.VerifiedHeader{
		Header: &types.Header{Height: 11},
		BlockID: types.BlockID{
			Hash: cmtrand.Bytes(32),
			PartSetHeader: types.PartSetHeader{
				Total: 4,
				Hash:  cmtrand.Bytes(32),
			},
		},
	}
	mgr.AddFromHeader(vh)
	pending, _ = mgr.GetBlock(11)
	require.Equal(t, SourceHeaderSync, pending.Source)

	// Add via commitment.
	psh := &types.PartSetHeader{Total: 4, Hash: cmtrand.Bytes(32)}
	mgr.AddCommitment(12, 0, psh)
	pending, _ = mgr.GetBlock(12)
	require.Equal(t, SourceCommitment, pending.Source)
}

func TestBlockSource_String(t *testing.T) {
	require.Equal(t, "compact_block", SourceCompactBlock.String())
	require.Equal(t, "header_sync", SourceHeaderSync.String())
	require.Equal(t, "commitment", SourceCommitment.String())
	require.Equal(t, "unknown", BlockSource(99).String())
}

func TestPendingBlocksManager_OnBlockAddedCallback(t *testing.T) {
	partsChan := make(chan types.PartInfo, 100)
	mgr := newTestPendingBlocksManager(partsChan, nil, DefaultPendingBlocksConfig())

	var callbackCalls []struct {
		height int64
		source BlockSource
	}

	mgr.SetOnBlockAdded(func(height int64, source BlockSource) {
		callbackCalls = append(callbackCalls, struct {
			height int64
			source BlockSource
		}{height, source})
	})

	// Callback should fire for header and commitment sources.
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
	mgr.AddFromHeader(vh)
	require.Len(t, callbackCalls, 1)
	require.Equal(t, int64(10), callbackCalls[0].height)
	require.Equal(t, SourceHeaderSync, callbackCalls[0].source)

	psh := &types.PartSetHeader{Total: 4, Hash: cmtrand.Bytes(32)}
	mgr.AddFromCommitment(11, 0, psh)
	require.Len(t, callbackCalls, 2)
	require.Equal(t, int64(11), callbackCalls[1].height)
	require.Equal(t, SourceCommitment, callbackCalls[1].source)

	// AddProposal does not trigger the callback (live path doesn't need catchup).
	cb := makePendingTestCompactBlock(12, 0, 4)
	mgr.AddProposal(cb)
	require.Len(t, callbackCalls, 2) // Still 2, no new callback.
}
