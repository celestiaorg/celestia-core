package propagation

import (
	"testing"
	"time"

	proptypes "github.com/cometbft/cometbft/consensus/propagation/types"
	"github.com/cometbft/cometbft/libs/log"
	cmtrand "github.com/cometbft/cometbft/libs/rand"
	"github.com/cometbft/cometbft/state"
	"github.com/cometbft/cometbft/types"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// Phase 9.1: Single Node Blocksync Integration Tests
// ============================================================================

// TestSingleNodeBlocksync verifies that a single lagging node can catch up
// to an ahead node using the PendingBlocksManager and BlockDeliveryManager.
//
// This test validates:
// 1. PendingBlocksManager correctly tracks blocks from headers
// 2. Parts are downloaded and verified correctly
// 3. BlockDeliveryManager delivers blocks in strictly increasing order
// 4. Node syncs to the target height
func TestSingleNodeBlocksync(t *testing.T) {
	p2pCfg := defaultTestP2PConf()
	cleanup, _, sm := state.SetupTestCase(t)
	t.Cleanup(func() {
		cleanup(t)
	})

	// Create reactors - n1 is ahead, n2 needs to catch up
	reactors, _ := createTestReactors(2, p2pCfg, false, "")
	ahead := reactors[0]
	lagging := reactors[1]

	// Number of blocks the ahead node has
	const numBlocks = 5

	// Store blocks and their data on the ahead node
	blocks := make(map[int64]*testBlockData, numBlocks)

	// Create blocks 1-5 on the ahead node
	for height := int64(1); height <= numBlocks; height++ {
		prop, ps, block, metaData := createTestProposal(t, sm, height, 0, 2, 100000)
		cb, parity := createCompactBlock(t, prop, ps, metaData)

		// Add block to ahead node
		added := ahead.AddProposal(cb)
		require.True(t, added, "failed to add proposal at height %d", height)

		// Fill in the parts
		_, parts, _, has := ahead.getAllState(height, 0, true)
		require.True(t, has)
		parts.SetProposalData(ps, parity)

		blocks[height] = &testBlockData{
			cb:     cb,
			ps:     ps,
			parity: parity,
			block:  block,
			prop:   prop,
		}

		// Advance the ahead node's height
		ahead.pmtx.Lock()
		ahead.height = height + 1
		ahead.pmtx.Unlock()
	}

	// Setup the lagging node with PendingBlocksManager
	logger := log.TestingLogger()
	pendingBlocks := NewPendingBlocksManager(logger, nil, PendingBlocksConfig{
		MaxConcurrent: 100,
		MemoryBudget:  1 << 30, // 1 GiB
	})

	// Create a mock HeaderSyncReader that provides headers for the blocks
	mockHS := &mockHeaderSyncReaderWithBlocks{
		blocks:   blocks,
		height:   numBlocks,
		caughtUp: true,
	}
	pendingBlocks.SetHeaderSyncReader(mockHS)
	pendingBlocks.SetLastCommittedHeight(0)

	// Create the BlockDeliveryManager
	blockDelivery := NewBlockDeliveryManager(pendingBlocks, 1, logger)
	blockDelivery.Start()
	defer blockDelivery.Stop()

	// Configure the lagging node with the pending blocks manager
	lagging.pendingBlocks = pendingBlocks
	lagging.hsReader = mockHS

	// Fill capacity from headers - this should add all blocks
	fetched := pendingBlocks.TryFillCapacity()
	require.Equal(t, numBlocks, fetched, "should fetch all %d headers", numBlocks)

	// Verify blocks were added to pending
	require.Equal(t, numBlocks, pendingBlocks.BlockCount())
	require.Equal(t, int64(1), pendingBlocks.LowestHeight())
	require.Equal(t, int64(numBlocks), pendingBlocks.HighestHeight())

	// Now simulate receiving parts from the ahead node
	// In a real scenario, this happens via the have/want protocol
	for height := int64(1); height <= numBlocks; height++ {
		bd := blocks[height]

		// Send all original parts for this block
		for i := uint32(0); i < bd.ps.Total(); i++ {
			part := bd.ps.GetPart(int(i))
			require.NotNil(t, part, "part %d at height %d should exist", i, height)

			recoveryPart := &proptypes.RecoveryPart{
				Height: height,
				Round:  0,
				Index:  i,
				Data:   part.Bytes.Bytes(),
			}

			// Get the proof from the compact block
			proof := bd.cb.GetProof(i)
			require.NotNil(t, proof, "proof for part %d at height %d should exist", i, height)

			added, complete, err := pendingBlocks.HandlePart(height, 0, recoveryPart, proof)
			require.NoError(t, err, "HandlePart should not error for height %d part %d", height, i)
			require.True(t, added, "part should be added for height %d part %d", height, i)

			if i == bd.ps.Total()-1 {
				require.True(t, complete, "block should be complete after last part at height %d", height)
			}
		}
	}

	// Wait for blocks to be delivered
	deliveredBlocks := make([]*CompletedBlock, 0, numBlocks)
	timeout := time.After(5 * time.Second)

	for len(deliveredBlocks) < numBlocks {
		select {
		case block := <-blockDelivery.BlockChan():
			deliveredBlocks = append(deliveredBlocks, block)
		case <-timeout:
			t.Fatalf("timeout waiting for blocks, got %d/%d", len(deliveredBlocks), numBlocks)
		}
	}

	// Verify blocks were delivered in order
	for i, block := range deliveredBlocks {
		expectedHeight := int64(i + 1)
		require.Equal(t, expectedHeight, block.Height,
			"block %d should have height %d, got %d", i, expectedHeight, block.Height)
	}

	// Verify all delivered blocks have complete parts
	for _, block := range deliveredBlocks {
		require.True(t, block.Parts.IsComplete(),
			"block at height %d should have complete parts", block.Height)
	}
}

// TestSingleNodeBlocksync_OutOfOrderParts verifies that blocks are delivered
// in order even when parts arrive out of order across different heights.
func TestSingleNodeBlocksync_OutOfOrderParts(t *testing.T) {
	p2pCfg := defaultTestP2PConf()
	cleanup, _, sm := state.SetupTestCase(t)
	t.Cleanup(func() {
		cleanup(t)
	})

	reactors, _ := createTestReactors(1, p2pCfg, false, "")
	_ = reactors[0] // ahead node (not used in this test)

	const numBlocks = 3

	blocks := make(map[int64]*testBlockData, numBlocks)

	// Create blocks
	for height := int64(1); height <= numBlocks; height++ {
		prop, ps, _, metaData := createTestProposal(t, sm, height, 0, 2, 100000)
		cb, parity := createCompactBlock(t, prop, ps, metaData)
		blocks[height] = &testBlockData{cb: cb, ps: ps, parity: parity}
	}

	// Setup manager
	logger := log.TestingLogger()
	pendingBlocks := NewPendingBlocksManager(logger, nil, PendingBlocksConfig{})

	mockHS := &mockHeaderSyncReaderWithBlocks{
		blocks:   blocks,
		height:   numBlocks,
		caughtUp: true,
	}
	pendingBlocks.SetHeaderSyncReader(mockHS)
	pendingBlocks.SetLastCommittedHeight(0)

	blockDelivery := NewBlockDeliveryManager(pendingBlocks, 1, logger)
	blockDelivery.Start()
	defer blockDelivery.Stop()

	// Fill capacity
	pendingBlocks.TryFillCapacity()

	// Send parts in reverse height order: height 3, then 2, then 1
	for height := int64(numBlocks); height >= 1; height-- {
		bd := blocks[height]
		for i := uint32(0); i < bd.ps.Total(); i++ {
			part := bd.ps.GetPart(int(i))
			proof := bd.cb.GetProof(i)

			recoveryPart := &proptypes.RecoveryPart{
				Height: height,
				Round:  0,
				Index:  i,
				Data:   part.Bytes.Bytes(),
			}

			_, _, err := pendingBlocks.HandlePart(height, 0, recoveryPart, proof)
			require.NoError(t, err)
		}
	}

	// Despite sending in reverse order, blocks should be delivered in order
	deliveredBlocks := make([]*CompletedBlock, 0, numBlocks)
	timeout := time.After(5 * time.Second)

	for len(deliveredBlocks) < numBlocks {
		select {
		case block := <-blockDelivery.BlockChan():
			deliveredBlocks = append(deliveredBlocks, block)
		case <-timeout:
			t.Fatalf("timeout waiting for blocks, got %d/%d", len(deliveredBlocks), numBlocks)
		}
	}

	// Verify strict order
	for i, block := range deliveredBlocks {
		expectedHeight := int64(i + 1)
		require.Equal(t, expectedHeight, block.Height,
			"block %d should have height %d, got %d", i, expectedHeight, block.Height)
	}
}

// TestSingleNodeBlocksync_PartialProgress verifies that the system handles
// partial block completion correctly - blocks with gaps don't prevent delivery
// of earlier complete blocks.
func TestSingleNodeBlocksync_PartialProgress(t *testing.T) {
	p2pCfg := defaultTestP2PConf()
	cleanup, _, sm := state.SetupTestCase(t)
	t.Cleanup(func() {
		cleanup(t)
	})

	reactors, _ := createTestReactors(1, p2pCfg, false, "")
	_ = reactors[0]

	const numBlocks = 3

	blocks := make(map[int64]*testBlockData, numBlocks)

	for height := int64(1); height <= numBlocks; height++ {
		prop, ps, _, metaData := createTestProposal(t, sm, height, 0, 2, 100000)
		cb, parity := createCompactBlock(t, prop, ps, metaData)
		blocks[height] = &testBlockData{cb: cb, ps: ps, parity: parity}
	}

	logger := log.TestingLogger()
	pendingBlocks := NewPendingBlocksManager(logger, nil, PendingBlocksConfig{})

	mockHS := &mockHeaderSyncReaderWithBlocks{
		blocks:   blocks,
		height:   numBlocks,
		caughtUp: true,
	}
	pendingBlocks.SetHeaderSyncReader(mockHS)
	pendingBlocks.SetLastCommittedHeight(0)

	blockDelivery := NewBlockDeliveryManager(pendingBlocks, 1, logger)
	blockDelivery.Start()
	defer blockDelivery.Stop()

	pendingBlocks.TryFillCapacity()

	// Complete block 1 first
	bd1 := blocks[1]
	for i := uint32(0); i < bd1.ps.Total(); i++ {
		part := bd1.ps.GetPart(int(i))
		proof := bd1.cb.GetProof(i)
		recoveryPart := &proptypes.RecoveryPart{
			Height: 1,
			Round:  0,
			Index:  i,
			Data:   part.Bytes.Bytes(),
		}
		_, _, err := pendingBlocks.HandlePart(1, 0, recoveryPart, proof)
		require.NoError(t, err)
	}

	// Block 1 should be delivered immediately
	select {
	case block := <-blockDelivery.BlockChan():
		require.Equal(t, int64(1), block.Height)
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for block 1")
	}

	// Complete block 3 (skipping block 2)
	bd3 := blocks[3]
	for i := uint32(0); i < bd3.ps.Total(); i++ {
		part := bd3.ps.GetPart(int(i))
		proof := bd3.cb.GetProof(i)
		recoveryPart := &proptypes.RecoveryPart{
			Height: 3,
			Round:  0,
			Index:  i,
			Data:   part.Bytes.Bytes(),
		}
		_, _, err := pendingBlocks.HandlePart(3, 0, recoveryPart, proof)
		require.NoError(t, err)
	}

	// Block 3 should NOT be delivered yet (waiting for block 2)
	select {
	case block := <-blockDelivery.BlockChan():
		t.Fatalf("block %d should not be delivered before block 2", block.Height)
	case <-time.After(500 * time.Millisecond):
		// Expected - block 3 is buffered
	}

	// Verify block 3 is buffered
	require.Equal(t, 1, blockDelivery.PendingCount(), "block 3 should be buffered")

	// Now complete block 2
	bd2 := blocks[2]
	for i := uint32(0); i < bd2.ps.Total(); i++ {
		part := bd2.ps.GetPart(int(i))
		proof := bd2.cb.GetProof(i)
		recoveryPart := &proptypes.RecoveryPart{
			Height: 2,
			Round:  0,
			Index:  i,
			Data:   part.Bytes.Bytes(),
		}
		_, _, err := pendingBlocks.HandlePart(2, 0, recoveryPart, proof)
		require.NoError(t, err)
	}

	// Both blocks 2 and 3 should be delivered now, in order
	deliveredBlocks := make([]*CompletedBlock, 0, 2)
	timeout := time.After(2 * time.Second)

	for len(deliveredBlocks) < 2 {
		select {
		case block := <-blockDelivery.BlockChan():
			deliveredBlocks = append(deliveredBlocks, block)
		case <-timeout:
			t.Fatalf("timeout waiting for blocks 2 and 3, got %d", len(deliveredBlocks))
		}
	}

	require.Equal(t, int64(2), deliveredBlocks[0].Height, "first delivered should be height 2")
	require.Equal(t, int64(3), deliveredBlocks[1].Height, "second delivered should be height 3")
}

// TestSingleNodeBlocksync_StateConsistency verifies that the manager state
// remains consistent throughout the sync process.
func TestSingleNodeBlocksync_StateConsistency(t *testing.T) {
	p2pCfg := defaultTestP2PConf()
	cleanup, _, sm := state.SetupTestCase(t)
	t.Cleanup(func() {
		cleanup(t)
	})

	reactors, _ := createTestReactors(1, p2pCfg, false, "")
	_ = reactors[0]

	const numBlocks = 5

	blocks := make(map[int64]*testBlockData, numBlocks)

	for height := int64(1); height <= numBlocks; height++ {
		prop, ps, _, metaData := createTestProposal(t, sm, height, 0, 2, 100000)
		cb, parity := createCompactBlock(t, prop, ps, metaData)
		blocks[height] = &testBlockData{cb: cb, ps: ps, parity: parity}
	}

	logger := log.TestingLogger()
	pendingBlocks := NewPendingBlocksManager(logger, nil, PendingBlocksConfig{})

	mockHS := &mockHeaderSyncReaderWithBlocks{
		blocks:   blocks,
		height:   numBlocks,
		caughtUp: true,
	}
	pendingBlocks.SetHeaderSyncReader(mockHS)
	pendingBlocks.SetLastCommittedHeight(0)

	// Verify initial state
	require.Equal(t, 0, pendingBlocks.BlockCount())
	require.True(t, pendingBlocks.HasCapacity())

	// Fill capacity
	fetched := pendingBlocks.TryFillCapacity()
	require.Equal(t, numBlocks, fetched)
	require.Equal(t, numBlocks, pendingBlocks.BlockCount())

	// Verify all blocks are tracked
	for height := int64(1); height <= numBlocks; height++ {
		require.True(t, pendingBlocks.HasHeight(height), "should track height %d", height)
		pb := pendingBlocks.GetBlock(height)
		require.NotNil(t, pb)
		require.True(t, pb.HeaderVerified, "block %d should have verified header", height)
		require.Equal(t, BlockStateActive, pb.State)
	}

	// Complete all blocks
	for height := int64(1); height <= numBlocks; height++ {
		bd := blocks[height]
		for i := uint32(0); i < bd.ps.Total(); i++ {
			part := bd.ps.GetPart(int(i))
			proof := bd.cb.GetProof(i)
			recoveryPart := &proptypes.RecoveryPart{
				Height: height,
				Round:  0,
				Index:  i,
				Data:   part.Bytes.Bytes(),
			}
			_, _, err := pendingBlocks.HandlePart(height, 0, recoveryPart, proof)
			require.NoError(t, err)
		}
	}

	// Verify all blocks are complete
	for height := int64(1); height <= numBlocks; height++ {
		pb := pendingBlocks.GetBlock(height)
		require.NotNil(t, pb)
		require.Equal(t, BlockStateComplete, pb.State, "block %d should be complete", height)
	}

	// Prune committed blocks
	pendingBlocks.Prune(3)

	// Verify pruning
	require.Equal(t, 2, pendingBlocks.BlockCount(), "should have 2 blocks after pruning")
	require.False(t, pendingBlocks.HasHeight(1), "height 1 should be pruned")
	require.False(t, pendingBlocks.HasHeight(2), "height 2 should be pruned")
	require.False(t, pendingBlocks.HasHeight(3), "height 3 should be pruned")
	require.True(t, pendingBlocks.HasHeight(4), "height 4 should remain")
	require.True(t, pendingBlocks.HasHeight(5), "height 5 should remain")
	require.Equal(t, int64(4), pendingBlocks.LowestHeight())
}

// ============================================================================
// Mock Implementations for Integration Tests
// ============================================================================

// testBlockData holds all the data for a single test block.
type testBlockData struct {
	cb     *proptypes.CompactBlock
	ps     *types.PartSet
	parity *types.PartSet
	block  *types.Block
	prop   *types.Proposal
}

// mockHeaderSyncReaderWithBlocks implements HeaderSyncReader for integration tests.
type mockHeaderSyncReaderWithBlocks struct {
	blocks   map[int64]*testBlockData
	height   int64
	caughtUp bool
}

func (m *mockHeaderSyncReaderWithBlocks) GetVerifiedHeader(height int64) (*types.Header, *types.BlockID, bool) {
	if height > m.height {
		return nil, nil, false
	}

	bd, exists := m.blocks[height]
	if !exists {
		return nil, nil, false
	}

	header := &types.Header{
		Height:          height,
		ChainID:         TestChainID,
		LastBlockID:     types.BlockID{},
		DataHash:        cmtrand.Bytes(32),
		ValidatorsHash:  cmtrand.Bytes(32),
		AppHash:         cmtrand.Bytes(32),
		ConsensusHash:   cmtrand.Bytes(32),
		LastResultsHash: cmtrand.Bytes(32),
	}
	blockID := bd.cb.Proposal.BlockID
	return header, &blockID, true
}

func (m *mockHeaderSyncReaderWithBlocks) GetCommit(height int64) *types.Commit {
	if height > m.height {
		return nil
	}
	// Return a mock commit
	return &types.Commit{
		Height: height,
		Round:  0,
		BlockID: types.BlockID{
			Hash:          cmtrand.Bytes(32),
			PartSetHeader: types.PartSetHeader{Total: 1, Hash: cmtrand.Bytes(32)},
		},
	}
}

func (m *mockHeaderSyncReaderWithBlocks) Height() int64 {
	return m.height
}

func (m *mockHeaderSyncReaderWithBlocks) MaxPeerHeight() int64 {
	return m.height
}

func (m *mockHeaderSyncReaderWithBlocks) IsCaughtUp() bool {
	return m.caughtUp
}
