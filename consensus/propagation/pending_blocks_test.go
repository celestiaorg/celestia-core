package propagation

import (
	"testing"
	"time"

	proptypes "github.com/cometbft/cometbft/consensus/propagation/types"
	"github.com/cometbft/cometbft/crypto/merkle"
	"github.com/cometbft/cometbft/internal/test"
	"github.com/cometbft/cometbft/libs/log"
	cmtrand "github.com/cometbft/cometbft/libs/rand"
	"github.com/cometbft/cometbft/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPendingBlockCreation(t *testing.T) {
	blockID := test.MakeBlockID()
	parts := proptypes.NewCombinedPartSetFromOriginal(
		types.NewPartSetFromHeader(blockID.PartSetHeader, types.BlockPartSizeBytes),
		false,
	)

	pb := &PendingBlock{
		Height:    10,
		Round:     1,
		Source:    SourceHeaderSync,
		BlockID:   blockID,
		Parts:     parts,
		CreatedAt: time.Now(),
		State:     BlockStateActive,
	}

	require.False(t, pb.CanUseProofCache())
	require.True(t, pb.NeedsInlineProofs())
	require.Equal(t, BlockStateActive, pb.State)

	pb.CompactBlock = &proptypes.CompactBlock{
		Proposal: types.Proposal{
			Height:  pb.Height,
			Round:   pb.Round,
			BlockID: pb.BlockID,
		},
		PartsHashes: [][]byte{{0x1}},
	}

	require.True(t, pb.CanUseProofCache())
	require.False(t, pb.NeedsInlineProofs())
}

func TestPendingBlockStateTransitions(t *testing.T) {
	pb := &PendingBlock{State: BlockStateActive}
	require.Equal(t, BlockStateActive, pb.State)

	pb.State = BlockStateComplete
	require.Equal(t, BlockStateComplete, pb.State)
}

func TestManagerCreation(t *testing.T) {
	manager := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{})

	require.Equal(t, defaultMaxConcurrent, manager.config.MaxConcurrent)
	require.Equal(t, defaultMemoryBudget, manager.config.MemoryBudget)
	require.NotNil(t, manager.blocks)
	require.NotNil(t, manager.completedBlocks)
	assert.Empty(t, manager.heights)
}

func TestManagerHeightOrdering(t *testing.T) {
	manager := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{})

	for _, h := range []int64{10, 5, 8, 5} {
		manager.mtx.Lock()
		manager.addBlock(&PendingBlock{Height: h})
		manager.mtx.Unlock()
	}

	require.Equal(t, []int64{5, 8, 10}, manager.heights)
	require.Len(t, manager.blocks, 3)
}

// Helper to create a compact block for testing
func makeTestCompactBlock(height int64, round int32, totalParts uint32) *proptypes.CompactBlock {
	partsHashes := make([][]byte, totalParts*2) // original + parity
	for i := range partsHashes {
		partsHashes[i] = cmtrand.Bytes(32)
	}

	return &proptypes.CompactBlock{
		BpHash:      cmtrand.Bytes(32),
		Signature:   cmtrand.Bytes(64),
		LastLen:     100,
		PartsHashes: partsHashes,
		Proposal: types.Proposal{
			Height: height,
			Round:  round,
			BlockID: types.BlockID{
				Hash: cmtrand.Bytes(32),
				PartSetHeader: types.PartSetHeader{
					Total: totalParts,
					Hash:  cmtrand.Bytes(32),
				},
			},
		},
	}
}

func TestAddProposal_NewBlock(t *testing.T) {
	manager := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{})
	cb := makeTestCompactBlock(10, 1, 5)

	added, err := manager.AddProposal(cb)
	require.NoError(t, err)
	require.True(t, added)

	// Verify block was added
	manager.mtx.RLock()
	defer manager.mtx.RUnlock()
	require.Contains(t, manager.blocks, int64(10))
	pb := manager.blocks[10]
	require.Equal(t, int64(10), pb.Height)
	require.Equal(t, int32(1), pb.Round)
	require.Equal(t, SourceCompactBlock, pb.Source)
	require.NotNil(t, pb.CompactBlock)
	require.True(t, pb.CanUseProofCache())
	require.Equal(t, BlockStateActive, pb.State)
}

func TestAddProposal_Duplicate(t *testing.T) {
	manager := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{})
	cb := makeTestCompactBlock(10, 1, 5)

	// Add first time
	added, err := manager.AddProposal(cb)
	require.NoError(t, err)
	require.True(t, added)

	// Add duplicate
	added, err = manager.AddProposal(cb)
	require.NoError(t, err)
	require.False(t, added, "duplicate should be rejected")
}

func TestAddProposal_AttachToHeader(t *testing.T) {
	manager := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{})

	// First add via header
	blockID := types.BlockID{
		Hash: cmtrand.Bytes(32),
		PartSetHeader: types.PartSetHeader{
			Total: 5,
			Hash:  cmtrand.Bytes(32),
		},
	}
	header := &types.Header{Height: 10}
	commit := &types.Commit{Height: 10}

	added, err := manager.AddFromHeader(header, blockID, commit)
	require.NoError(t, err)
	require.True(t, added)

	// Verify no CompactBlock yet
	manager.mtx.RLock()
	pb := manager.blocks[10]
	manager.mtx.RUnlock()
	require.Nil(t, pb.CompactBlock)
	require.True(t, pb.NeedsInlineProofs())

	// Now add matching CompactBlock
	cb := &proptypes.CompactBlock{
		BpHash:      cmtrand.Bytes(32),
		Signature:   cmtrand.Bytes(64),
		LastLen:     100,
		PartsHashes: make([][]byte, 10),
		Proposal: types.Proposal{
			Height:  10,
			Round:   1,
			BlockID: blockID, // Same BlockID as header
		},
	}
	for i := range cb.PartsHashes {
		cb.PartsHashes[i] = cmtrand.Bytes(32)
	}

	added, err = manager.AddProposal(cb)
	require.NoError(t, err)
	require.True(t, added)

	// Verify CompactBlock was attached
	manager.mtx.RLock()
	pb = manager.blocks[10]
	manager.mtx.RUnlock()
	require.NotNil(t, pb.CompactBlock)
	require.True(t, pb.CanUseProofCache())
	require.Equal(t, SourceHeaderSync, pb.Source, "source should remain HeaderSync")
}

func TestAddProposal_AttachToCommitment(t *testing.T) {
	manager := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{})

	// First add via commitment
	psh := types.PartSetHeader{
		Total: 5,
		Hash:  cmtrand.Bytes(32),
	}
	added := manager.AddFromCommitment(10, 1, &psh)
	require.True(t, added)

	// Create matching CompactBlock
	cb := &proptypes.CompactBlock{
		BpHash:      cmtrand.Bytes(32),
		Signature:   cmtrand.Bytes(64),
		LastLen:     100,
		PartsHashes: make([][]byte, 10),
		Proposal: types.Proposal{
			Height: 10,
			Round:  1,
			BlockID: types.BlockID{
				Hash:          cmtrand.Bytes(32),
				PartSetHeader: psh, // Same PSH as commitment
			},
		},
	}
	for i := range cb.PartsHashes {
		cb.PartsHashes[i] = cmtrand.Bytes(32)
	}

	addedCB, err := manager.AddProposal(cb)
	require.NoError(t, err)
	require.True(t, addedCB)

	// Verify CompactBlock was attached
	manager.mtx.RLock()
	pb := manager.blocks[10]
	manager.mtx.RUnlock()
	require.NotNil(t, pb.CompactBlock)
	require.Equal(t, SourceCommitment, pb.Source, "source should remain Commitment")
}

func TestAddProposal_HashMismatch(t *testing.T) {
	manager := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{})

	// First add via header
	blockID := types.BlockID{
		Hash: cmtrand.Bytes(32),
		PartSetHeader: types.PartSetHeader{
			Total: 5,
			Hash:  cmtrand.Bytes(32),
		},
	}
	header := &types.Header{Height: 10}
	added, err := manager.AddFromHeader(header, blockID, nil)
	require.NoError(t, err)
	require.True(t, added)

	// Try to add CompactBlock with different BlockID
	cb := makeTestCompactBlock(10, 1, 5) // Different hash
	_, err = manager.AddProposal(cb)
	require.Error(t, err)
	require.Contains(t, err.Error(), "mismatch")
}

func TestAddProposal_PSHMismatch(t *testing.T) {
	manager := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{})

	// First add via commitment
	psh := types.PartSetHeader{
		Total: 5,
		Hash:  cmtrand.Bytes(32),
	}
	manager.AddFromCommitment(10, 1, &psh)

	// Try to add CompactBlock with different PSH
	cb := makeTestCompactBlock(10, 1, 5) // Different PSH
	_, err := manager.AddProposal(cb)
	require.Error(t, err)
	require.Contains(t, err.Error(), "PSH mismatch")
}

func TestAddProposal_CapacityLimit(t *testing.T) {
	manager := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{
		MaxConcurrent: 2,
		MemoryBudget:  1 << 30, // 1GiB
	})

	// Add first block
	cb1 := makeTestCompactBlock(10, 1, 5)
	added, err := manager.AddProposal(cb1)
	require.NoError(t, err)
	require.True(t, added)

	// Add second block
	cb2 := makeTestCompactBlock(11, 1, 5)
	added, err = manager.AddProposal(cb2)
	require.NoError(t, err)
	require.True(t, added)

	// Third should fail - at capacity
	cb3 := makeTestCompactBlock(12, 1, 5)
	added, err = manager.AddProposal(cb3)
	require.Error(t, err)
	require.False(t, added)
	require.Contains(t, err.Error(), "capacity exceeded")
}

func TestAddProposal_MemoryLimit(t *testing.T) {
	// Set memory budget to exactly fit 1 block with 5 parts
	// Memory estimate is BlockPartSizeBytes * totalParts * 2 (for parity)
	memBudget := int64(types.BlockPartSizeBytes) * 5 * 2
	manager := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{
		MaxConcurrent: 100,
		MemoryBudget:  memBudget,
	})

	// Add first block with 5 parts - should fit exactly
	cb1 := makeTestCompactBlock(10, 1, 5)
	added, err := manager.AddProposal(cb1)
	require.NoError(t, err)
	require.True(t, added)

	// Add second block - should fail (not enough memory)
	cb2 := makeTestCompactBlock(11, 1, 5)
	added, err = manager.AddProposal(cb2)
	require.Error(t, err)
	require.False(t, added)
	require.Contains(t, err.Error(), "capacity exceeded")
}

// ============================================================================
// AddFromHeader Tests
// ============================================================================

func TestAddFromHeader_NewBlock(t *testing.T) {
	manager := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{})

	blockID := test.MakeBlockID()
	header := &types.Header{Height: 10}
	commit := &types.Commit{Height: 10}

	added, err := manager.AddFromHeader(header, blockID, commit)
	require.NoError(t, err)
	require.True(t, added)

	// Verify block was added
	manager.mtx.RLock()
	defer manager.mtx.RUnlock()
	require.Contains(t, manager.blocks, int64(10))
	pb := manager.blocks[10]
	require.Equal(t, int64(10), pb.Height)
	require.Equal(t, SourceHeaderSync, pb.Source)
	require.True(t, pb.HeaderVerified)
	require.Equal(t, commit, pb.Commit)
	require.Nil(t, pb.CompactBlock)
	require.True(t, pb.NeedsInlineProofs())
}

func TestAddFromHeader_Duplicate(t *testing.T) {
	manager := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{})

	blockID := test.MakeBlockID()
	header := &types.Header{Height: 10}

	// Add first time
	added, err := manager.AddFromHeader(header, blockID, nil)
	require.NoError(t, err)
	require.True(t, added)

	// Add duplicate
	added, err = manager.AddFromHeader(header, blockID, nil)
	require.NoError(t, err)
	require.False(t, added, "duplicate should be rejected")
}

func TestAddFromHeader_UpgradeCommitment(t *testing.T) {
	manager := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{})

	// First add via commitment
	psh := types.PartSetHeader{
		Total: 5,
		Hash:  cmtrand.Bytes(32),
	}
	added := manager.AddFromCommitment(10, 1, &psh)
	require.True(t, added)

	// Verify not yet header verified
	manager.mtx.RLock()
	pb := manager.blocks[10]
	manager.mtx.RUnlock()
	require.False(t, pb.HeaderVerified)

	// Now add matching header
	blockID := types.BlockID{
		Hash:          cmtrand.Bytes(32),
		PartSetHeader: psh,
	}
	header := &types.Header{Height: 10}
	commit := &types.Commit{Height: 10}

	addedH, err := manager.AddFromHeader(header, blockID, commit)
	require.NoError(t, err)
	require.True(t, addedH)

	// Verify upgrade
	manager.mtx.RLock()
	pb = manager.blocks[10]
	manager.mtx.RUnlock()
	require.True(t, pb.HeaderVerified)
	require.Equal(t, commit, pb.Commit)
	require.Equal(t, blockID, pb.BlockID)
	require.Equal(t, SourceCommitment, pb.Source, "source should remain Commitment")
}

func TestAddFromHeader_ValidateCompactBlock(t *testing.T) {
	manager := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{})

	// First add via CompactBlock
	cb := makeTestCompactBlock(10, 1, 5)
	added, err := manager.AddProposal(cb)
	require.NoError(t, err)
	require.True(t, added)

	// Try to add header with different BlockID
	differentBlockID := test.MakeBlockID()
	header := &types.Header{Height: 10}

	_, err = manager.AddFromHeader(header, differentBlockID, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "mismatch")
}

func TestAddFromHeader_PSHMismatch(t *testing.T) {
	manager := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{})

	// First add via commitment
	psh := types.PartSetHeader{
		Total: 5,
		Hash:  cmtrand.Bytes(32),
	}
	manager.AddFromCommitment(10, 1, &psh)

	// Try to add header with different PSH
	differentPSH := types.PartSetHeader{
		Total: 5,
		Hash:  cmtrand.Bytes(32), // Different hash
	}
	blockID := types.BlockID{
		Hash:          cmtrand.Bytes(32),
		PartSetHeader: differentPSH,
	}
	header := &types.Header{Height: 10}

	_, err := manager.AddFromHeader(header, blockID, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "PSH mismatch")
}

// ============================================================================
// AddFromCommitment Tests
// ============================================================================

func TestAddFromCommitment_NewBlock(t *testing.T) {
	manager := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{})

	psh := types.PartSetHeader{
		Total: 5,
		Hash:  cmtrand.Bytes(32),
	}

	added := manager.AddFromCommitment(10, 1, &psh)
	require.True(t, added)

	// Verify block was added
	manager.mtx.RLock()
	defer manager.mtx.RUnlock()
	require.Contains(t, manager.blocks, int64(10))
	pb := manager.blocks[10]
	require.Equal(t, int64(10), pb.Height)
	require.Equal(t, int32(1), pb.Round)
	require.Equal(t, SourceCommitment, pb.Source)
	require.True(t, pb.HasCommitment)
	require.False(t, pb.HeaderVerified)
	require.Nil(t, pb.CompactBlock)
	require.True(t, pb.NeedsInlineProofs())
}

func TestAddFromCommitment_ExistingBlock(t *testing.T) {
	manager := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{})

	// First add via header
	blockID := test.MakeBlockID()
	header := &types.Header{Height: 10}
	added, err := manager.AddFromHeader(header, blockID, nil)
	require.NoError(t, err)
	require.True(t, added)

	// Verify no commitment flag
	manager.mtx.RLock()
	pb := manager.blocks[10]
	manager.mtx.RUnlock()
	require.False(t, pb.HasCommitment)

	// Add commitment
	addedC := manager.AddFromCommitment(10, 1, &blockID.PartSetHeader)
	require.False(t, addedC, "should return false for existing block")

	// Verify commitment flag is now set
	manager.mtx.RLock()
	pb = manager.blocks[10]
	manager.mtx.RUnlock()
	require.True(t, pb.HasCommitment)
}

func TestAddFromCommitment_BypassLimits(t *testing.T) {
	// Set memory budget to fit exactly 1 block with 5 parts
	memBudget := int64(types.BlockPartSizeBytes) * 5 * 2
	manager := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{
		MaxConcurrent: 1,
		MemoryBudget:  memBudget,
	})

	// Add first block via proposal (uses normal limits)
	cb := makeTestCompactBlock(10, 1, 5)
	added, err := manager.AddProposal(cb)
	require.NoError(t, err)
	require.True(t, added)

	// Second proposal should fail (at capacity)
	cb2 := makeTestCompactBlock(11, 1, 5)
	_, err = manager.AddProposal(cb2)
	require.Error(t, err)

	// But commitment should bypass limits
	psh := types.PartSetHeader{
		Total: 5,
		Hash:  cmtrand.Bytes(32),
	}
	added = manager.AddFromCommitment(12, 1, &psh)
	require.True(t, added, "commitment should bypass capacity limits")

	manager.mtx.RLock()
	require.Contains(t, manager.blocks, int64(12))
	manager.mtx.RUnlock()
}

func TestAddFromCommitment_PSHMismatch(t *testing.T) {
	manager := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{})

	// First add via header
	blockID := test.MakeBlockID()
	header := &types.Header{Height: 10}
	added, err := manager.AddFromHeader(header, blockID, nil)
	require.NoError(t, err)
	require.True(t, added)

	// Add commitment with different PSH - should log error but accept
	differentPSH := types.PartSetHeader{
		Total: 10,
		Hash:  cmtrand.Bytes(32),
	}
	manager.AddFromCommitment(10, 1, &differentPSH)

	// Commitment is authoritative - PSH should be updated
	manager.mtx.RLock()
	pb := manager.blocks[10]
	manager.mtx.RUnlock()
	require.True(t, pb.HasCommitment)
	require.Equal(t, differentPSH, pb.BlockID.PartSetHeader)
}

// ============================================================================
// Memory Estimation Tests
// ============================================================================

func TestMemoryEstimation(t *testing.T) {
	manager := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{})

	psh := types.PartSetHeader{Total: 10}
	expected := int64(types.BlockPartSizeBytes) * 10 * 2

	actual := manager.estimateBlockMemory(&psh)
	require.Equal(t, expected, actual)
}

// ============================================================================
// Phase 3: HandlePart Tests
// ============================================================================

// Helper to create a valid partset with real merkle proofs
func makeTestPartSet(t *testing.T, totalParts int) (*types.PartSet, []byte) {
	t.Helper()
	// Create random data that will generate the desired number of parts
	data := cmtrand.Bytes(int(types.BlockPartSizeBytes) * totalParts)
	partSet, err := types.NewPartSetFromData(data, types.BlockPartSizeBytes)
	require.NoError(t, err)
	require.Equal(t, uint32(totalParts), partSet.Total())
	return partSet, data
}

// Helper to create a RecoveryPart from a types.Part
func makeRecoveryPart(height int64, round int32, part *types.Part) *proptypes.RecoveryPart {
	return &proptypes.RecoveryPart{
		Height: height,
		Round:  round,
		Index:  part.Index,
		Data:   part.Bytes,
		Proof:  &part.Proof,
	}
}

func TestHandlePart_Valid(t *testing.T) {
	manager := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{})

	// Create a real partset with valid merkle proofs
	partSet, _ := makeTestPartSet(t, 5)

	// Add a block from commitment (needs inline proofs)
	height := int64(10)
	round := int32(1)
	psh := partSet.Header()
	manager.AddFromCommitment(height, round, &psh)

	// Get first part from the source partset
	sourcePart := partSet.GetPart(0)
	require.NotNil(t, sourcePart)

	recoveryPart := makeRecoveryPart(height, round, sourcePart)

	// Handle the part with its proof
	added, complete, err := manager.HandlePart(height, round, recoveryPart, &sourcePart.Proof)
	require.NoError(t, err)
	require.True(t, added)
	require.False(t, complete, "should not be complete after 1 of 5 parts")

	// Verify the part was added
	pb := manager.GetBlock(height)
	require.NotNil(t, pb)
	require.True(t, pb.Parts.HasPart(0))
}

func TestHandlePart_InvalidProof(t *testing.T) {
	manager := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{})

	// Create a real partset
	partSet, _ := makeTestPartSet(t, 5)
	psh := partSet.Header()

	// Add block from commitment
	height := int64(10)
	round := int32(1)
	manager.AddFromCommitment(height, round, &psh)

	// Create a part with invalid proof
	sourcePart := partSet.GetPart(0)
	recoveryPart := makeRecoveryPart(height, round, sourcePart)

	// Create an invalid proof (wrong leaf hash)
	invalidProof := sourcePart.Proof
	invalidProof.LeafHash = cmtrand.Bytes(32) // Wrong hash

	// Handle should fail due to invalid proof
	added, _, err := manager.HandlePart(height, round, recoveryPart, &invalidProof)
	require.Error(t, err)
	require.False(t, added)
}

func TestHandlePart_UnknownHeight(t *testing.T) {
	manager := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{})

	// Create a part for a height that doesn't exist
	recoveryPart := &proptypes.RecoveryPart{
		Height: 999,
		Round:  0,
		Index:  0,
		Data:   cmtrand.Bytes(100),
	}

	// Should return false, nil (gracefully ignored)
	added, complete, err := manager.HandlePart(999, 0, recoveryPart, nil)
	require.NoError(t, err)
	require.False(t, added)
	require.False(t, complete)
}

func TestHandlePart_Completion(t *testing.T) {
	manager := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{})

	// Create a partset with 3 parts for simpler testing
	partSet, _ := makeTestPartSet(t, 3)
	psh := partSet.Header()

	height := int64(10)
	round := int32(1)
	manager.AddFromCommitment(height, round, &psh)

	// Add all parts except the last one
	for i := 0; i < 2; i++ {
		sourcePart := partSet.GetPart(i)
		recoveryPart := makeRecoveryPart(height, round, sourcePart)
		added, complete, err := manager.HandlePart(height, round, recoveryPart, &sourcePart.Proof)
		require.NoError(t, err)
		require.True(t, added)
		require.False(t, complete, "should not be complete yet")
	}

	// Consume any messages from completedBlocks channel (non-blocking)
	select {
	case <-manager.CompletedBlocksChan():
		t.Fatal("should not have completion yet")
	default:
	}

	// Add the last part
	lastPart := partSet.GetPart(2)
	recoveryPart := makeRecoveryPart(height, round, lastPart)
	added, complete, err := manager.HandlePart(height, round, recoveryPart, &lastPart.Proof)
	require.NoError(t, err)
	require.True(t, added)
	require.True(t, complete, "should be complete now")

	// Verify state transition
	pb := manager.GetBlock(height)
	require.Equal(t, BlockStateComplete, pb.State)

	// Verify completion was sent to channel
	select {
	case completedBlock := <-manager.CompletedBlocksChan():
		require.Equal(t, height, completedBlock.Height)
		require.Equal(t, round, completedBlock.Round)
		require.NotNil(t, completedBlock.Parts)
		require.True(t, completedBlock.Parts.IsComplete())
	default:
		t.Fatal("expected completion message in channel")
	}
}

func TestHandlePart_ProofCacheFromCompactBlock(t *testing.T) {
	manager := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{})

	// Create a real partset
	partSet, _ := makeTestPartSet(t, 5)
	psh := partSet.Header()

	// Create CompactBlock with proof cache
	cb := &proptypes.CompactBlock{
		BpHash:      cmtrand.Bytes(32),
		Signature:   cmtrand.Bytes(64),
		LastLen:     100,
		PartsHashes: make([][]byte, 10), // 5 original + 5 parity
		Proposal: types.Proposal{
			Height: 10,
			Round:  1,
			BlockID: types.BlockID{
				Hash:          cmtrand.Bytes(32),
				PartSetHeader: psh,
			},
		},
	}
	for i := range cb.PartsHashes {
		cb.PartsHashes[i] = cmtrand.Bytes(32)
	}

	// Build proof cache from actual parts
	proofs := make([]*merkle.Proof, partSet.Total())
	for i := 0; i < int(partSet.Total()); i++ {
		part := partSet.GetPart(i)
		proof := part.Proof
		proofs[i] = &proof
	}
	cb.SetProofCache(proofs)

	// Add the proposal
	added, err := manager.AddProposal(cb)
	require.NoError(t, err)
	require.True(t, added)

	// Verify we can use proof cache
	pb := manager.GetBlock(10)
	require.True(t, pb.CanUseProofCache())

	// Handle part WITHOUT inline proof - should use cache
	sourcePart := partSet.GetPart(0)
	recoveryPart := &proptypes.RecoveryPart{
		Height: 10,
		Round:  1,
		Index:  0,
		Data:   sourcePart.Bytes,
		Proof:  nil, // No inline proof
	}

	addedPart, _, err := manager.HandlePart(10, 1, recoveryPart, nil)
	require.NoError(t, err)
	require.True(t, addedPart)
}

func TestHandlePart_DuplicateOrStale(t *testing.T) {
	manager := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{})

	// Create a partset
	partSet, _ := makeTestPartSet(t, 5)
	psh := partSet.Header()

	height := int64(10)
	round := int32(1)
	manager.AddFromCommitment(height, round, &psh)

	// Add a part
	sourcePart := partSet.GetPart(0)
	recoveryPart := makeRecoveryPart(height, round, sourcePart)
	added, _, err := manager.HandlePart(height, round, recoveryPart, &sourcePart.Proof)
	require.NoError(t, err)
	require.True(t, added)

	// Try to add the same part again (duplicate)
	added, complete, err := manager.HandlePart(height, round, recoveryPart, &sourcePart.Proof)
	require.NoError(t, err)
	require.False(t, added, "duplicate part should be ignored")
	require.False(t, complete)
}

func TestHandlePart_MissingProofWithoutCompactBlock(t *testing.T) {
	manager := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{})

	// Add block from commitment (needs inline proofs, no CompactBlock)
	psh := types.PartSetHeader{
		Total: 5,
		Hash:  cmtrand.Bytes(32),
	}
	manager.AddFromCommitment(10, 1, &psh)

	// Try to add part without proof
	recoveryPart := &proptypes.RecoveryPart{
		Height: 10,
		Round:  1,
		Index:  0,
		Data:   cmtrand.Bytes(100),
		Proof:  nil, // No proof
	}

	added, _, err := manager.HandlePart(10, 1, recoveryPart, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "missing inline proof")
	require.False(t, added)
}

// ============================================================================
// Phase 3: GetMissingParts Tests
// ============================================================================

func TestGetMissingParts_Ordering(t *testing.T) {
	manager := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{})

	// Add blocks at various heights (out of order)
	psh := types.PartSetHeader{Total: 5, Hash: cmtrand.Bytes(32)}
	manager.AddFromCommitment(15, 0, &psh)
	psh2 := types.PartSetHeader{Total: 5, Hash: cmtrand.Bytes(32)}
	manager.AddFromCommitment(10, 0, &psh2)
	psh3 := types.PartSetHeader{Total: 5, Hash: cmtrand.Bytes(32)}
	manager.AddFromCommitment(12, 0, &psh3)

	// Get missing parts
	missing := manager.GetMissingParts(10)

	// Should be ordered by height (lowest first)
	require.Len(t, missing, 3)
	require.Equal(t, int64(10), missing[0].Height)
	require.Equal(t, int64(12), missing[1].Height)
	require.Equal(t, int64(15), missing[2].Height)
}

func TestGetMissingParts_OnlyActive(t *testing.T) {
	manager := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{})

	// Create two partsets
	partSet1, _ := makeTestPartSet(t, 3)
	psh1 := partSet1.Header()

	psh2 := types.PartSetHeader{Total: 5, Hash: cmtrand.Bytes(32)}

	// Add two blocks
	manager.AddFromCommitment(10, 0, &psh1)
	manager.AddFromCommitment(11, 0, &psh2)

	// Complete block at height 10
	for i := 0; i < int(partSet1.Total()); i++ {
		sourcePart := partSet1.GetPart(i)
		recoveryPart := makeRecoveryPart(10, 0, sourcePart)
		_, _, err := manager.HandlePart(10, 0, recoveryPart, &sourcePart.Proof)
		require.NoError(t, err)
	}

	// Drain the completed channel
	select {
	case <-manager.CompletedBlocksChan():
	default:
	}

	// Get missing parts - should only return height 11 (height 10 is complete)
	missing := manager.GetMissingParts(10)
	require.Len(t, missing, 1)
	require.Equal(t, int64(11), missing[0].Height)
}

func TestGetMissingParts_Limit(t *testing.T) {
	manager := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{})

	// Add many blocks
	for h := int64(10); h < 20; h++ {
		psh := types.PartSetHeader{Total: 5, Hash: cmtrand.Bytes(32)}
		manager.AddFromCommitment(h, 0, &psh)
	}

	// Request only 3
	missing := manager.GetMissingParts(3)
	require.Len(t, missing, 3)

	// Should be the lowest 3 heights
	require.Equal(t, int64(10), missing[0].Height)
	require.Equal(t, int64(11), missing[1].Height)
	require.Equal(t, int64(12), missing[2].Height)
}

func TestGetMissingParts_NeedsProofsFlag(t *testing.T) {
	manager := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{})

	// Add block from commitment (needs proofs)
	psh := types.PartSetHeader{Total: 5, Hash: cmtrand.Bytes(32)}
	manager.AddFromCommitment(10, 0, &psh)

	// Add block from compact block (doesn't need proofs)
	cb := makeTestCompactBlock(11, 0, 5)
	_, err := manager.AddProposal(cb)
	require.NoError(t, err)

	missing := manager.GetMissingParts(10)
	require.Len(t, missing, 2)

	// Block from commitment needs proofs
	require.Equal(t, int64(10), missing[0].Height)
	require.True(t, missing[0].NeedsProofs)

	// Block from compact block doesn't need proofs
	require.Equal(t, int64(11), missing[1].Height)
	require.False(t, missing[1].NeedsProofs)
}

func TestGetMissingParts_HasCommitmentFlag(t *testing.T) {
	manager := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{})

	// Add block from commitment
	psh := types.PartSetHeader{Total: 5, Hash: cmtrand.Bytes(32)}
	manager.AddFromCommitment(10, 0, &psh)

	// Add block from header (no commitment)
	blockID := test.MakeBlockID()
	header := &types.Header{Height: 11}
	_, err := manager.AddFromHeader(header, blockID, nil)
	require.NoError(t, err)

	missing := manager.GetMissingParts(10)
	require.Len(t, missing, 2)

	// Block from commitment
	require.Equal(t, int64(10), missing[0].Height)
	require.True(t, missing[0].HasCommitment)

	// Block from header
	require.Equal(t, int64(11), missing[1].Height)
	require.False(t, missing[1].HasCommitment)
}

// ============================================================================
// Helper Method Tests
// ============================================================================

func TestGetBlock(t *testing.T) {
	manager := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{})

	psh := types.PartSetHeader{Total: 5, Hash: cmtrand.Bytes(32)}
	manager.AddFromCommitment(10, 1, &psh)

	// Get existing block
	pb := manager.GetBlock(10)
	require.NotNil(t, pb)
	require.Equal(t, int64(10), pb.Height)

	// Get non-existing block
	pb = manager.GetBlock(999)
	require.Nil(t, pb)
}

func TestHasHeight(t *testing.T) {
	manager := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{})

	psh := types.PartSetHeader{Total: 5, Hash: cmtrand.Bytes(32)}
	manager.AddFromCommitment(10, 1, &psh)

	require.True(t, manager.HasHeight(10))
	require.False(t, manager.HasHeight(999))
}

// ============================================================================
// Phase 4: Memory Management & Pruning Tests
// ============================================================================

func TestHasCapacity_UnderLimit(t *testing.T) {
	manager := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{
		MaxConcurrent: 10,
		MemoryBudget:  1 << 30, // 1GiB
	})

	require.True(t, manager.HasCapacity())
	require.Equal(t, 10, manager.AvailableCapacity())

	// Add one block
	psh := types.PartSetHeader{Total: 5, Hash: cmtrand.Bytes(32)}
	manager.AddFromCommitment(10, 0, &psh)

	// Still has capacity
	require.True(t, manager.HasCapacity())
	require.Equal(t, 9, manager.AvailableCapacity())
}

func TestHasCapacity_AtLimit(t *testing.T) {
	manager := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{
		MaxConcurrent: 2,
		MemoryBudget:  1 << 30, // 1GiB
	})

	// Add blocks to reach max concurrent limit
	psh1 := types.PartSetHeader{Total: 5, Hash: cmtrand.Bytes(32)}
	manager.AddFromCommitment(10, 0, &psh1)
	psh2 := types.PartSetHeader{Total: 5, Hash: cmtrand.Bytes(32)}
	manager.AddFromCommitment(11, 0, &psh2)

	// At capacity
	require.False(t, manager.HasCapacity())
	require.Equal(t, 0, manager.AvailableCapacity())
}

func TestHasCapacity_MemoryLimit(t *testing.T) {
	// Set memory budget to fit ~50 parts worth of average blocks
	// HasCapacity uses 100 parts * 2 * BlockPartSizeBytes as average estimate
	avgBlockMemory := int64(types.BlockPartSizeBytes) * 100 * 2
	memBudget := avgBlockMemory / 2 // Half of one "average" block

	manager := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{
		MaxConcurrent: 100,
		MemoryBudget:  memBudget,
	})

	// Should not have capacity even though MaxConcurrent allows it
	// because adding an average block would exceed memory budget
	require.False(t, manager.HasCapacity())
}

func TestAvailableCapacity(t *testing.T) {
	manager := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{
		MaxConcurrent: 5,
		MemoryBudget:  1 << 30,
	})

	require.Equal(t, 5, manager.AvailableCapacity())

	// Add some blocks
	for h := int64(10); h < 13; h++ {
		psh := types.PartSetHeader{Total: 5, Hash: cmtrand.Bytes(32)}
		manager.AddFromCommitment(h, 0, &psh)
	}

	require.Equal(t, 2, manager.AvailableCapacity())
}

func TestCurrentMemory(t *testing.T) {
	manager := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{})

	require.Equal(t, int64(0), manager.CurrentMemory())

	// Add a block with known parts count
	psh := types.PartSetHeader{Total: 10, Hash: cmtrand.Bytes(32)}
	manager.AddFromCommitment(10, 0, &psh)

	// Memory should be partSize * totalParts * 2
	expectedMemory := int64(types.BlockPartSizeBytes) * 10 * 2
	require.Equal(t, expectedMemory, manager.CurrentMemory())
}

func TestBlockCount(t *testing.T) {
	manager := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{})

	require.Equal(t, 0, manager.BlockCount())

	// Add some blocks
	for h := int64(10); h < 15; h++ {
		psh := types.PartSetHeader{Total: 5, Hash: cmtrand.Bytes(32)}
		manager.AddFromCommitment(h, 0, &psh)
	}

	require.Equal(t, 5, manager.BlockCount())
}

func TestPrune_RemovesOld(t *testing.T) {
	manager := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{})

	// Add blocks at heights 10, 11, 12, 13, 14
	for h := int64(10); h < 15; h++ {
		psh := types.PartSetHeader{Total: 5, Hash: cmtrand.Bytes(32)}
		manager.AddFromCommitment(h, 0, &psh)
	}

	require.Equal(t, 5, manager.BlockCount())

	// Prune up to and including height 12
	manager.Prune(12)

	// Should have removed heights 10, 11, 12
	require.Equal(t, 2, manager.BlockCount())
	require.False(t, manager.HasHeight(10))
	require.False(t, manager.HasHeight(11))
	require.False(t, manager.HasHeight(12))
	require.True(t, manager.HasHeight(13))
	require.True(t, manager.HasHeight(14))
}

func TestPrune_KeepsNew(t *testing.T) {
	manager := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{})

	// Add blocks at heights 10, 11, 12
	for h := int64(10); h < 13; h++ {
		psh := types.PartSetHeader{Total: 5, Hash: cmtrand.Bytes(32)}
		manager.AddFromCommitment(h, 0, &psh)
	}

	// Prune up to height 5 (below all blocks)
	manager.Prune(5)

	// All blocks should remain
	require.Equal(t, 3, manager.BlockCount())
	require.True(t, manager.HasHeight(10))
	require.True(t, manager.HasHeight(11))
	require.True(t, manager.HasHeight(12))
}

func TestPrune_MemoryUpdate(t *testing.T) {
	manager := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{})

	// Add blocks with known memory
	for h := int64(10); h < 15; h++ {
		psh := types.PartSetHeader{Total: 10, Hash: cmtrand.Bytes(32)}
		manager.AddFromCommitment(h, 0, &psh)
	}

	// Each block uses partSize * 10 * 2 bytes
	perBlockMemory := int64(types.BlockPartSizeBytes) * 10 * 2
	require.Equal(t, perBlockMemory*5, manager.CurrentMemory())

	// Prune 3 blocks (10, 11, 12)
	manager.Prune(12)

	// Memory should be reduced by 3 blocks worth
	require.Equal(t, perBlockMemory*2, manager.CurrentMemory())
}

func TestPrune_EmptyManager(t *testing.T) {
	manager := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{})

	// Should not panic
	manager.Prune(100)

	require.Equal(t, 0, manager.BlockCount())
	require.Equal(t, int64(0), manager.CurrentMemory())
}

func TestPrune_AllBlocks(t *testing.T) {
	manager := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{})

	// Add blocks
	for h := int64(10); h < 15; h++ {
		psh := types.PartSetHeader{Total: 5, Hash: cmtrand.Bytes(32)}
		manager.AddFromCommitment(h, 0, &psh)
	}

	// Prune all
	manager.Prune(100)

	require.Equal(t, 0, manager.BlockCount())
	require.Equal(t, int64(0), manager.CurrentMemory())
	require.Equal(t, int64(0), manager.LowestHeight())
	require.Equal(t, int64(0), manager.HighestHeight())
}

func TestLowestHeight(t *testing.T) {
	manager := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{})

	// Empty manager
	require.Equal(t, int64(0), manager.LowestHeight())

	// Add blocks out of order
	for _, h := range []int64{15, 10, 12} {
		psh := types.PartSetHeader{Total: 5, Hash: cmtrand.Bytes(32)}
		manager.AddFromCommitment(h, 0, &psh)
	}

	require.Equal(t, int64(10), manager.LowestHeight())
}

func TestHighestHeight(t *testing.T) {
	manager := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{})

	// Empty manager
	require.Equal(t, int64(0), manager.HighestHeight())

	// Add blocks out of order
	for _, h := range []int64{15, 10, 12} {
		psh := types.PartSetHeader{Total: 5, Hash: cmtrand.Bytes(32)}
		manager.AddFromCommitment(h, 0, &psh)
	}

	require.Equal(t, int64(15), manager.HighestHeight())
}

// ============================================================================
// Phase 5: Headersync Integration Tests
// ============================================================================

// mockHeaderSyncReader is a mock implementation of HeaderSyncReader for testing.
type mockHeaderSyncReader struct {
	headers       map[int64]*types.Header
	blockIDs      map[int64]*types.BlockID
	commits       map[int64]*types.Commit
	height        int64
	maxPeerHeight int64
	caughtUp      bool
}

func newMockHeaderSyncReader() *mockHeaderSyncReader {
	return &mockHeaderSyncReader{
		headers:       make(map[int64]*types.Header),
		blockIDs:      make(map[int64]*types.BlockID),
		commits:       make(map[int64]*types.Commit),
		height:        0,
		maxPeerHeight: 0,
		caughtUp:      false,
	}
}

func (m *mockHeaderSyncReader) GetVerifiedHeader(height int64) (*types.Header, *types.BlockID, bool) {
	header, ok := m.headers[height]
	if !ok {
		return nil, nil, false
	}
	blockID, ok := m.blockIDs[height]
	if !ok {
		return nil, nil, false
	}
	return header, blockID, true
}

func (m *mockHeaderSyncReader) GetCommit(height int64) *types.Commit {
	return m.commits[height]
}

func (m *mockHeaderSyncReader) Height() int64 {
	return m.height
}

func (m *mockHeaderSyncReader) MaxPeerHeight() int64 {
	return m.maxPeerHeight
}

func (m *mockHeaderSyncReader) IsCaughtUp() bool {
	return m.caughtUp
}

// addHeader adds a verified header at the given height to the mock.
func (m *mockHeaderSyncReader) addHeader(height int64, numParts uint32) {
	psh := types.PartSetHeader{
		Total: numParts,
		Hash:  cmtrand.Bytes(32),
	}
	blockID := types.BlockID{
		Hash:          cmtrand.Bytes(32),
		PartSetHeader: psh,
	}
	m.headers[height] = &types.Header{Height: height}
	m.blockIDs[height] = &blockID
	m.commits[height] = &types.Commit{Height: height}
	if height > m.height {
		m.height = height
	}
}

func TestSetHeaderSyncReader(t *testing.T) {
	manager := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{})

	mock := newMockHeaderSyncReader()
	manager.SetHeaderSyncReader(mock)

	// Should not panic when reader is set
	fetched := manager.TryFillCapacity()
	require.Equal(t, 0, fetched, "should fetch 0 when no headers available")
}

func TestSetLastCommittedHeight(t *testing.T) {
	manager := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{})

	require.Equal(t, int64(0), manager.GetLastCommittedHeight())

	manager.SetLastCommittedHeight(100)
	require.Equal(t, int64(100), manager.GetLastCommittedHeight())
}

func TestLowestNeededHeight(t *testing.T) {
	manager := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{})

	// No committed height, no pending blocks
	require.Equal(t, int64(1), manager.LowestNeededHeight())

	// Set committed height
	manager.SetLastCommittedHeight(50)
	require.Equal(t, int64(51), manager.LowestNeededHeight())

	// Add a pending block below the needed height (gap scenario)
	psh := types.PartSetHeader{Total: 5, Hash: cmtrand.Bytes(32)}
	manager.AddFromCommitment(48, 0, &psh) // Below committed height

	// Should use the lower pending block height
	require.Equal(t, int64(48), manager.LowestNeededHeight())

	// Add higher pending block
	psh2 := types.PartSetHeader{Total: 5, Hash: cmtrand.Bytes(32)}
	manager.AddFromCommitment(55, 0, &psh2)

	// Still returns lowest pending
	require.Equal(t, int64(48), manager.LowestNeededHeight())
}

func TestTryFillCapacity_NoReader(t *testing.T) {
	manager := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{})

	// No reader set
	fetched := manager.TryFillCapacity()
	require.Equal(t, 0, fetched)
}

func TestTryFillCapacity_Available(t *testing.T) {
	manager := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{
		MaxConcurrent: 10,
		MemoryBudget:  1 << 30,
	})

	mock := newMockHeaderSyncReader()
	// Add headers at heights 1, 2, 3
	for h := int64(1); h <= 3; h++ {
		mock.addHeader(h, 5)
	}
	manager.SetHeaderSyncReader(mock)

	// Should fetch all 3
	fetched := manager.TryFillCapacity()
	require.Equal(t, 3, fetched)
	require.Equal(t, 3, manager.BlockCount())

	// Verify blocks were added
	for h := int64(1); h <= 3; h++ {
		require.True(t, manager.HasHeight(h))
		pb := manager.GetBlock(h)
		require.NotNil(t, pb)
		require.True(t, pb.HeaderVerified)
		require.NotNil(t, pb.Commit)
	}
}

func TestTryFillCapacity_StartsFromLowest(t *testing.T) {
	manager := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{
		MaxConcurrent: 10,
		MemoryBudget:  1 << 30,
	})

	// Set committed height to 10
	manager.SetLastCommittedHeight(10)

	mock := newMockHeaderSyncReader()
	// Add headers at heights 11, 12, 13, 14, 15
	for h := int64(11); h <= 15; h++ {
		mock.addHeader(h, 5)
	}
	manager.SetHeaderSyncReader(mock)

	fetched := manager.TryFillCapacity()
	require.Equal(t, 5, fetched)

	// Verify they were fetched in order from 11
	require.Equal(t, int64(11), manager.LowestHeight())
	require.Equal(t, int64(15), manager.HighestHeight())
}

func TestTryFillCapacity_StopsAtCapacity(t *testing.T) {
	manager := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{
		MaxConcurrent: 3, // Only allow 3 blocks
		MemoryBudget:  1 << 30,
	})

	mock := newMockHeaderSyncReader()
	// Add headers at heights 1-10
	for h := int64(1); h <= 10; h++ {
		mock.addHeader(h, 5)
	}
	manager.SetHeaderSyncReader(mock)

	// Should only fetch 3 due to capacity
	fetched := manager.TryFillCapacity()
	require.Equal(t, 3, fetched)
	require.Equal(t, 3, manager.BlockCount())

	// Should be the lowest 3
	require.True(t, manager.HasHeight(1))
	require.True(t, manager.HasHeight(2))
	require.True(t, manager.HasHeight(3))
	require.False(t, manager.HasHeight(4))
}

func TestTryFillCapacity_SkipsExisting(t *testing.T) {
	manager := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{
		MaxConcurrent: 10,
		MemoryBudget:  1 << 30,
	})

	// Pre-add a block from commitment at height 2
	psh := types.PartSetHeader{Total: 5, Hash: cmtrand.Bytes(32)}
	manager.AddFromCommitment(2, 0, &psh)

	mock := newMockHeaderSyncReader()
	// Add headers at heights 1, 2, 3
	for h := int64(1); h <= 3; h++ {
		mock.addHeader(h, 5)
	}
	manager.SetHeaderSyncReader(mock)

	// Should fetch 1 and 3, skip 2
	fetched := manager.TryFillCapacity()
	require.Equal(t, 2, fetched)
	require.Equal(t, 3, manager.BlockCount())

	// Height 2 should still be from commitment (not upgraded)
	pb := manager.GetBlock(2)
	require.NotNil(t, pb)
	require.Equal(t, SourceCommitment, pb.Source)
}

func TestTryFillCapacity_UpgradesCommitmentBlock(t *testing.T) {
	manager := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{
		MaxConcurrent: 10,
		MemoryBudget:  1 << 30,
	})

	mock := newMockHeaderSyncReader()
	mock.addHeader(1, 5)
	manager.SetHeaderSyncReader(mock)

	// Pre-add a block from commitment with matching PSH
	psh := mock.blockIDs[1].PartSetHeader
	manager.AddFromCommitment(1, 0, &psh)

	pb := manager.GetBlock(1)
	require.False(t, pb.HeaderVerified)
	require.Nil(t, pb.Commit)

	// Fetch - should upgrade the existing block
	fetched := manager.TryFillCapacity()
	require.Equal(t, 1, fetched)

	pb = manager.GetBlock(1)
	require.True(t, pb.HeaderVerified)
	require.NotNil(t, pb.Commit)
	require.Equal(t, SourceCommitment, pb.Source) // Source doesn't change
}

func TestTryFetchHeader_Available(t *testing.T) {
	manager := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{
		MaxConcurrent: 10,
		MemoryBudget:  1 << 30,
	})

	mock := newMockHeaderSyncReader()
	mock.addHeader(10, 5)
	manager.SetHeaderSyncReader(mock)

	// Fetch single header
	success := manager.TryFetchHeader(10)
	require.True(t, success)
	require.True(t, manager.HasHeight(10))
}

func TestTryFetchHeader_NotYetAvailable(t *testing.T) {
	manager := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{
		MaxConcurrent: 10,
		MemoryBudget:  1 << 30,
	})

	mock := newMockHeaderSyncReader()
	// Don't add header at height 10
	manager.SetHeaderSyncReader(mock)

	// Should return false
	success := manager.TryFetchHeader(10)
	require.False(t, success)
	require.False(t, manager.HasHeight(10))
}

func TestTryFetchHeader_HeaderWithoutCommit(t *testing.T) {
	manager := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{
		MaxConcurrent: 10,
		MemoryBudget:  1 << 30,
	})

	mock := newMockHeaderSyncReader()
	// Add header but NOT commit
	psh := types.PartSetHeader{Total: 5, Hash: cmtrand.Bytes(32)}
	blockID := types.BlockID{Hash: cmtrand.Bytes(32), PartSetHeader: psh}
	mock.headers[10] = &types.Header{Height: 10}
	mock.blockIDs[10] = &blockID
	// Don't add commit: mock.commits[10] = nil
	mock.height = 10
	manager.SetHeaderSyncReader(mock)

	// Should return false because commit is missing
	success := manager.TryFetchHeader(10)
	require.False(t, success)
	require.False(t, manager.HasHeight(10))
}

func TestTryFetchHeader_Idempotent(t *testing.T) {
	manager := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{
		MaxConcurrent: 10,
		MemoryBudget:  1 << 30,
	})

	mock := newMockHeaderSyncReader()
	mock.addHeader(10, 5)
	manager.SetHeaderSyncReader(mock)

	// First fetch
	success := manager.TryFetchHeader(10)
	require.True(t, success)

	// Second fetch - should return false (already have header)
	success = manager.TryFetchHeader(10)
	require.False(t, success)

	// But block count should still be 1
	require.Equal(t, 1, manager.BlockCount())
}

func TestOnBlockComplete_TriggersRefill(t *testing.T) {
	manager := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{
		MaxConcurrent: 2, // Limit to 2
		MemoryBudget:  1 << 30,
	})

	mock := newMockHeaderSyncReader()
	for h := int64(1); h <= 5; h++ {
		mock.addHeader(h, 5)
	}
	manager.SetHeaderSyncReader(mock)

	// Fill to capacity
	fetched := manager.TryFillCapacity()
	require.Equal(t, 2, fetched)
	require.Equal(t, 2, manager.BlockCount())
	require.True(t, manager.HasHeight(1))
	require.True(t, manager.HasHeight(2))

	// Simulate block 1 being committed: update lastCommittedHeight and prune
	manager.SetLastCommittedHeight(1)
	manager.Prune(1)
	require.Equal(t, 1, manager.BlockCount())
	require.False(t, manager.HasHeight(1))
	require.True(t, manager.HasHeight(2))

	// OnBlockComplete should trigger refill
	// Now lowest needed is 2 (lastCommittedHeight + 1), but we already have 2
	// So it should fetch 3
	manager.OnBlockComplete(1)

	// Should have fetched one more (height 3)
	require.Equal(t, 2, manager.BlockCount())
	require.True(t, manager.HasHeight(2))
	require.True(t, manager.HasHeight(3)) // Next in sequence
}

// ============================================================================
// Phase 6: BlockDeliveryManager Tests
// ============================================================================

func TestBlockDeliveryManager_Creation(t *testing.T) {
	manager := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{})
	delivery := NewBlockDeliveryManager(manager, 1, log.NewNopLogger())

	require.NotNil(t, delivery)
	require.Equal(t, int64(1), delivery.NextHeight())
	require.Equal(t, 0, delivery.PendingCount())
	require.NotNil(t, delivery.BlockChan())
}

func TestDelivery_InOrder(t *testing.T) {
	manager := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{})
	delivery := NewBlockDeliveryManager(manager, 1, log.NewNopLogger())
	delivery.Start()
	defer delivery.Stop()

	// Send blocks in order: 1, 2, 3
	for height := int64(1); height <= 3; height++ {
		manager.completedBlocks <- &CompletedBlock{
			Height:  height,
			Round:   0,
			BlockID: types.BlockID{Hash: cmtrand.Bytes(32)},
		}
	}

	// Receive and verify order
	for expectedHeight := int64(1); expectedHeight <= 3; expectedHeight++ {
		select {
		case block := <-delivery.BlockChan():
			require.Equal(t, expectedHeight, block.Height, "block should be delivered in order")
		case <-time.After(time.Second):
			t.Fatalf("timeout waiting for block %d", expectedHeight)
		}
	}

	require.Equal(t, int64(4), delivery.NextHeight())
}

func TestDelivery_WaitsForGaps(t *testing.T) {
	manager := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{})
	delivery := NewBlockDeliveryManager(manager, 1, log.NewNopLogger())
	delivery.Start()
	defer delivery.Stop()

	// Send block 3 first (out of order)
	manager.completedBlocks <- &CompletedBlock{
		Height:  3,
		Round:   0,
		BlockID: types.BlockID{Hash: cmtrand.Bytes(32)},
	}

	// Give time for processing
	time.Sleep(50 * time.Millisecond)

	// Block 3 should be buffered, not delivered yet
	require.Equal(t, 1, delivery.PendingCount(), "block 3 should be buffered")
	require.Equal(t, int64(1), delivery.NextHeight(), "still waiting for block 1")

	// Nothing should be on the channel yet
	select {
	case block := <-delivery.BlockChan():
		t.Fatalf("unexpected block delivered: %d", block.Height)
	default:
		// Good - no block delivered yet
	}

	// Now send block 1
	manager.completedBlocks <- &CompletedBlock{
		Height:  1,
		Round:   0,
		BlockID: types.BlockID{Hash: cmtrand.Bytes(32)},
	}

	// Block 1 should be delivered
	select {
	case block := <-delivery.BlockChan():
		require.Equal(t, int64(1), block.Height)
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for block 1")
	}

	// Small delay to let the delivery loop update nextHeight after sending
	time.Sleep(20 * time.Millisecond)

	// Still waiting for block 2
	require.Equal(t, int64(2), delivery.NextHeight())
	require.Equal(t, 1, delivery.PendingCount(), "block 3 still buffered")
}

func TestDelivery_BatchDelivery(t *testing.T) {
	manager := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{})
	delivery := NewBlockDeliveryManager(manager, 1, log.NewNopLogger())
	delivery.Start()
	defer delivery.Stop()

	// Send blocks out of order: 3, 2, 5, 4, 1
	heights := []int64{3, 2, 5, 4, 1}
	for _, h := range heights {
		manager.completedBlocks <- &CompletedBlock{
			Height:  h,
			Round:   0,
			BlockID: types.BlockID{Hash: cmtrand.Bytes(32)},
		}
		// Small delay to ensure processing
		time.Sleep(10 * time.Millisecond)
	}

	// All should be delivered in order now
	for expectedHeight := int64(1); expectedHeight <= 5; expectedHeight++ {
		select {
		case block := <-delivery.BlockChan():
			require.Equal(t, expectedHeight, block.Height, "blocks should be delivered in strict order")
		case <-time.After(time.Second):
			t.Fatalf("timeout waiting for block %d", expectedHeight)
		}
	}

	require.Equal(t, int64(6), delivery.NextHeight())
	require.Equal(t, 0, delivery.PendingCount())
}

func TestDelivery_DuplicateOrStaleCompletion(t *testing.T) {
	manager := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{})
	delivery := NewBlockDeliveryManager(manager, 5, log.NewNopLogger()) // Start at height 5
	delivery.Start()
	defer delivery.Stop()

	// Send a stale block (height 3 < nextHeight 5)
	manager.completedBlocks <- &CompletedBlock{
		Height:  3,
		Round:   0,
		BlockID: types.BlockID{Hash: cmtrand.Bytes(32)},
	}

	time.Sleep(50 * time.Millisecond)

	// Should be ignored
	require.Equal(t, int64(5), delivery.NextHeight(), "nextHeight should be unchanged")
	require.Equal(t, 0, delivery.PendingCount(), "stale block should not be buffered")

	// Send block 5
	manager.completedBlocks <- &CompletedBlock{
		Height:  5,
		Round:   0,
		BlockID: types.BlockID{Hash: cmtrand.Bytes(32)},
	}

	select {
	case block := <-delivery.BlockChan():
		require.Equal(t, int64(5), block.Height)
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for block 5")
	}

	// Send duplicate block 6
	manager.completedBlocks <- &CompletedBlock{
		Height:  6,
		Round:   0,
		BlockID: types.BlockID{Hash: cmtrand.Bytes(32)},
	}

	select {
	case block := <-delivery.BlockChan():
		require.Equal(t, int64(6), block.Height)
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for block 6")
	}

	// Send a second "completion" for block 6 (which is now stale since nextHeight=7)
	manager.completedBlocks <- &CompletedBlock{
		Height:  6,
		Round:   0,
		BlockID: types.BlockID{Hash: cmtrand.Bytes(32)},
	}

	time.Sleep(50 * time.Millisecond)

	// Should not cause issues - nextHeight should still be 7
	require.Equal(t, int64(7), delivery.NextHeight())
}

func TestDelivery_Backpressure(t *testing.T) {
	manager := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{})
	delivery := NewBlockDeliveryManager(manager, 1, log.NewNopLogger())
	delivery.Start()
	defer delivery.Stop()

	// Fill the blockChan buffer (size 10) plus some extra
	// The delivery manager should buffer blocks that can't be sent
	for height := int64(1); height <= 15; height++ {
		manager.completedBlocks <- &CompletedBlock{
			Height:  height,
			Round:   0,
			BlockID: types.BlockID{Hash: cmtrand.Bytes(32)},
		}
	}

	// Give time for all completions to be processed
	time.Sleep(100 * time.Millisecond)

	// Now drain the channel and verify order
	for expectedHeight := int64(1); expectedHeight <= 15; expectedHeight++ {
		select {
		case block := <-delivery.BlockChan():
			require.Equal(t, expectedHeight, block.Height, "blocks should still be in order under backpressure")
		case <-time.After(time.Second):
			t.Fatalf("timeout waiting for block %d", expectedHeight)
		}
	}
}

func TestDelivery_SetNextHeight(t *testing.T) {
	manager := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{})
	delivery := NewBlockDeliveryManager(manager, 1, log.NewNopLogger())
	delivery.Start()
	defer delivery.Stop()

	// Buffer some blocks
	for _, h := range []int64{3, 4, 5, 6} {
		manager.completedBlocks <- &CompletedBlock{
			Height:  h,
			Round:   0,
			BlockID: types.BlockID{Hash: cmtrand.Bytes(32)},
		}
	}

	time.Sleep(50 * time.Millisecond)

	// All should be buffered waiting for height 1
	require.Equal(t, 4, delivery.PendingCount())

	// Now set next height to 5 (skipping 1-4)
	// This should:
	// 1. Delete blocks 3 and 4 as stale
	// 2. Immediately deliver blocks 5 and 6 since they're consecutive from nextHeight
	delivery.SetNextHeight(5)

	// Block 5 and 6 should be delivered immediately by SetNextHeight
	select {
	case block := <-delivery.BlockChan():
		require.Equal(t, int64(5), block.Height)
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for block 5")
	}

	select {
	case block := <-delivery.BlockChan():
		require.Equal(t, int64(6), block.Height)
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for block 6")
	}

	// After delivery, nextHeight should be 7 and no pending blocks
	time.Sleep(20 * time.Millisecond) // Allow delivery loop to update state
	require.Equal(t, int64(7), delivery.NextHeight())
	require.Equal(t, 0, delivery.PendingCount())
}

func TestDelivery_StopDuringDelivery(t *testing.T) {
	manager := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{})
	delivery := NewBlockDeliveryManager(manager, 1, log.NewNopLogger())
	delivery.Start()

	// Send a few blocks
	for h := int64(1); h <= 3; h++ {
		manager.completedBlocks <- &CompletedBlock{
			Height:  h,
			Round:   0,
			BlockID: types.BlockID{Hash: cmtrand.Bytes(32)},
		}
	}

	// Read one block
	select {
	case block := <-delivery.BlockChan():
		require.Equal(t, int64(1), block.Height)
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}

	// Stop should not panic or deadlock
	done := make(chan struct{})
	go func() {
		delivery.Stop()
		close(done)
	}()

	select {
	case <-done:
		// Good - stopped successfully
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() deadlocked")
	}
}

func TestDelivery_StartStopIdempotent(t *testing.T) {
	manager := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{})
	delivery := NewBlockDeliveryManager(manager, 1, log.NewNopLogger())

	// Multiple starts should be safe
	delivery.Start()
	delivery.Start()
	delivery.Start()

	// Send a block to verify it's working
	manager.completedBlocks <- &CompletedBlock{
		Height:  1,
		Round:   0,
		BlockID: types.BlockID{Hash: cmtrand.Bytes(32)},
	}

	select {
	case block := <-delivery.BlockChan():
		require.Equal(t, int64(1), block.Height)
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}

	// Multiple stops should be safe
	delivery.Stop()
	delivery.Stop()
	delivery.Stop()
}

// ============================================================================
// Step 6.5: GetBlockChan Tests
// ============================================================================

func TestNoOpPropagator_GetBlockChan(t *testing.T) {
	nop := NewNoOpPropagator()
	require.Nil(t, nop.GetBlockChan(), "NoOpPropagator.GetBlockChan should return nil")
}

func TestBlockDeliveryManager_BlockChan(t *testing.T) {
	manager := NewPendingBlocksManager(log.NewNopLogger(), nil, PendingBlocksConfig{})
	delivery := NewBlockDeliveryManager(manager, 1, log.NewNopLogger())

	// BlockChan should return a valid channel
	ch := delivery.BlockChan()
	require.NotNil(t, ch, "BlockDeliveryManager.BlockChan should return a valid channel")

	// Use the channel to verify it's a valid receive-only channel
	_ = ch
}
