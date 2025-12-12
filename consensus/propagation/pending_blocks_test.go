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
	added, err = manager.AddProposal(cb2)
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
