package propagation

import (
	"sync"
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

// ============================================================================
// Phase 9.2: Multi-Node Parallel Blocksync Integration Tests
// ============================================================================

// TestMultiNodeParallelBlocksync verifies that multiple lagging nodes can
// sync blocks in parallel using the PendingBlocksManager.
//
// This test validates:
// 1. Multiple nodes can track and sync blocks concurrently
// 2. Parts are downloaded from multiple peers
// 3. Both lagging nodes catch up to the target height
func TestMultiNodeParallelBlocksync(t *testing.T) {
	p2pCfg := defaultTestP2PConf()
	cleanup, _, sm := state.SetupTestCase(t)
	t.Cleanup(func() {
		cleanup(t)
	})

	// Create 4 reactors:
	// - reactors[0], reactors[1]: "ahead" nodes that have blocks
	// - reactors[2], reactors[3]: "lagging" nodes that need to sync
	reactors, _ := createTestReactors(4, p2pCfg, false, "")
	ahead1 := reactors[0]
	ahead2 := reactors[1]
	lagging1 := reactors[2]
	lagging2 := reactors[3]

	const numBlocks = 5

	// Store blocks on the ahead nodes
	blocks := make(map[int64]*testBlockData, numBlocks)

	for height := int64(1); height <= numBlocks; height++ {
		prop, ps, block, metaData := createTestProposal(t, sm, height, 0, 2, 100000)
		cb, parity := createCompactBlock(t, prop, ps, metaData)

		// Add to both ahead nodes
		added1 := ahead1.AddProposal(cb)
		require.True(t, added1, "ahead1 failed to add proposal at height %d", height)
		added2 := ahead2.AddProposal(cb)
		require.True(t, added2, "ahead2 failed to add proposal at height %d", height)

		// Fill in parts for both
		_, parts1, _, has1 := ahead1.getAllState(height, 0, true)
		require.True(t, has1)
		parts1.SetProposalData(ps, parity)

		_, parts2, _, has2 := ahead2.getAllState(height, 0, true)
		require.True(t, has2)
		parts2.SetProposalData(ps, parity)

		blocks[height] = &testBlockData{
			cb:     cb,
			ps:     ps,
			parity: parity,
			block:  block,
			prop:   prop,
		}

		// Advance both ahead nodes
		ahead1.pmtx.Lock()
		ahead1.height = height + 1
		ahead1.pmtx.Unlock()

		ahead2.pmtx.Lock()
		ahead2.height = height + 1
		ahead2.pmtx.Unlock()
	}

	// Setup both lagging nodes with PendingBlocksManager
	logger := log.TestingLogger()

	// Setup lagging1
	pendingBlocks1 := NewPendingBlocksManager(logger.With("node", "lagging1"), nil, PendingBlocksConfig{
		MaxConcurrent: 100,
		MemoryBudget:  1 << 30,
	})
	mockHS1 := &mockHeaderSyncReaderWithBlocks{
		blocks:   blocks,
		height:   numBlocks,
		caughtUp: true,
	}
	pendingBlocks1.SetHeaderSyncReader(mockHS1)
	pendingBlocks1.SetLastCommittedHeight(0)
	blockDelivery1 := NewBlockDeliveryManager(pendingBlocks1, 1, logger.With("node", "lagging1"))
	blockDelivery1.Start()
	defer blockDelivery1.Stop()
	lagging1.pendingBlocks = pendingBlocks1
	lagging1.hsReader = mockHS1

	// Setup lagging2
	pendingBlocks2 := NewPendingBlocksManager(logger.With("node", "lagging2"), nil, PendingBlocksConfig{
		MaxConcurrent: 100,
		MemoryBudget:  1 << 30,
	})
	mockHS2 := &mockHeaderSyncReaderWithBlocks{
		blocks:   blocks,
		height:   numBlocks,
		caughtUp: true,
	}
	pendingBlocks2.SetHeaderSyncReader(mockHS2)
	pendingBlocks2.SetLastCommittedHeight(0)
	blockDelivery2 := NewBlockDeliveryManager(pendingBlocks2, 1, logger.With("node", "lagging2"))
	blockDelivery2.Start()
	defer blockDelivery2.Stop()
	lagging2.pendingBlocks = pendingBlocks2
	lagging2.hsReader = mockHS2

	// Both lagging nodes fill capacity
	fetched1 := pendingBlocks1.TryFillCapacity()
	require.Equal(t, numBlocks, fetched1, "lagging1 should fetch all headers")

	fetched2 := pendingBlocks2.TryFillCapacity()
	require.Equal(t, numBlocks, fetched2, "lagging2 should fetch all headers")

	// Verify both have all blocks tracked
	require.Equal(t, numBlocks, pendingBlocks1.BlockCount())
	require.Equal(t, numBlocks, pendingBlocks2.BlockCount())

	// Simulate both lagging nodes receiving parts in parallel
	// lagging1 gets even parts, lagging2 gets odd parts initially
	// Then they get the remaining parts

	// First round: distribute parts
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

			// lagging1 gets even-indexed parts
			if i%2 == 0 {
				_, _, err := pendingBlocks1.HandlePart(height, 0, recoveryPart, proof)
				require.NoError(t, err)
			}

			// lagging2 gets odd-indexed parts
			if i%2 == 1 {
				_, _, err := pendingBlocks2.HandlePart(height, 0, recoveryPart, proof)
				require.NoError(t, err)
			}
		}
	}

	// Second round: complete all blocks
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

			// lagging1 gets remaining (odd) parts
			if i%2 == 1 {
				_, _, err := pendingBlocks1.HandlePart(height, 0, recoveryPart, proof)
				require.NoError(t, err)
			}

			// lagging2 gets remaining (even) parts
			if i%2 == 0 {
				_, _, err := pendingBlocks2.HandlePart(height, 0, recoveryPart, proof)
				require.NoError(t, err)
			}
		}
	}

	// Wait for all blocks to be delivered to both nodes
	timeout := time.After(5 * time.Second)
	deliveredToLagging1 := make([]*CompletedBlock, 0, numBlocks)
	deliveredToLagging2 := make([]*CompletedBlock, 0, numBlocks)

	for len(deliveredToLagging1) < numBlocks || len(deliveredToLagging2) < numBlocks {
		select {
		case block := <-blockDelivery1.BlockChan():
			deliveredToLagging1 = append(deliveredToLagging1, block)
		case block := <-blockDelivery2.BlockChan():
			deliveredToLagging2 = append(deliveredToLagging2, block)
		case <-timeout:
			t.Fatalf("timeout: lagging1 got %d/%d, lagging2 got %d/%d",
				len(deliveredToLagging1), numBlocks,
				len(deliveredToLagging2), numBlocks)
		}
	}

	// Verify both nodes received blocks in order
	for i, block := range deliveredToLagging1 {
		require.Equal(t, int64(i+1), block.Height,
			"lagging1: block %d should have height %d", i, i+1)
	}
	for i, block := range deliveredToLagging2 {
		require.Equal(t, int64(i+1), block.Height,
			"lagging2: block %d should have height %d", i, i+1)
	}
}

// TestMultiNodeParallelBlocksync_DifferentSpeeds verifies that nodes syncing
// at different speeds (receiving parts at different rates) still work correctly.
func TestMultiNodeParallelBlocksync_DifferentSpeeds(t *testing.T) {
	p2pCfg := defaultTestP2PConf()
	cleanup, _, sm := state.SetupTestCase(t)
	t.Cleanup(func() {
		cleanup(t)
	})

	reactors, _ := createTestReactors(3, p2pCfg, false, "")
	ahead := reactors[0]
	fastLagging := reactors[1]
	slowLagging := reactors[2]

	const numBlocks = 3

	blocks := make(map[int64]*testBlockData, numBlocks)

	for height := int64(1); height <= numBlocks; height++ {
		prop, ps, block, metaData := createTestProposal(t, sm, height, 0, 2, 100000)
		cb, parity := createCompactBlock(t, prop, ps, metaData)

		added := ahead.AddProposal(cb)
		require.True(t, added)

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

		ahead.pmtx.Lock()
		ahead.height = height + 1
		ahead.pmtx.Unlock()
	}

	logger := log.TestingLogger()

	// Setup fast lagging node
	fastPendingBlocks := NewPendingBlocksManager(logger.With("node", "fast"), nil, PendingBlocksConfig{})
	fastMockHS := &mockHeaderSyncReaderWithBlocks{
		blocks:   blocks,
		height:   numBlocks,
		caughtUp: true,
	}
	fastPendingBlocks.SetHeaderSyncReader(fastMockHS)
	fastPendingBlocks.SetLastCommittedHeight(0)
	fastDelivery := NewBlockDeliveryManager(fastPendingBlocks, 1, logger.With("node", "fast"))
	fastDelivery.Start()
	defer fastDelivery.Stop()
	fastLagging.pendingBlocks = fastPendingBlocks
	fastLagging.hsReader = fastMockHS

	// Setup slow lagging node
	slowPendingBlocks := NewPendingBlocksManager(logger.With("node", "slow"), nil, PendingBlocksConfig{})
	slowMockHS := &mockHeaderSyncReaderWithBlocks{
		blocks:   blocks,
		height:   numBlocks,
		caughtUp: true,
	}
	slowPendingBlocks.SetHeaderSyncReader(slowMockHS)
	slowPendingBlocks.SetLastCommittedHeight(0)
	slowDelivery := NewBlockDeliveryManager(slowPendingBlocks, 1, logger.With("node", "slow"))
	slowDelivery.Start()
	defer slowDelivery.Stop()
	slowLagging.pendingBlocks = slowPendingBlocks
	slowLagging.hsReader = slowMockHS

	// Both fill capacity
	fastPendingBlocks.TryFillCapacity()
	slowPendingBlocks.TryFillCapacity()

	// Fast node gets all parts for all blocks quickly
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
			_, _, err := fastPendingBlocks.HandlePart(height, 0, recoveryPart, proof)
			require.NoError(t, err)
		}
	}

	// Slow node only gets block 1 first
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
		_, _, err := slowPendingBlocks.HandlePart(1, 0, recoveryPart, proof)
		require.NoError(t, err)
	}

	// Fast node should have all blocks delivered
	fastDelivered := make([]*CompletedBlock, 0, numBlocks)
	timeout := time.After(2 * time.Second)
	for len(fastDelivered) < numBlocks {
		select {
		case block := <-fastDelivery.BlockChan():
			fastDelivered = append(fastDelivered, block)
		case <-timeout:
			t.Fatalf("fast node timeout, got %d/%d", len(fastDelivered), numBlocks)
		}
	}

	// Slow node should have block 1 delivered
	select {
	case block := <-slowDelivery.BlockChan():
		require.Equal(t, int64(1), block.Height, "slow node should receive block 1")
	case <-time.After(1 * time.Second):
		t.Fatal("slow node should have received block 1")
	}

	// Now slow node gets the rest
	for height := int64(2); height <= numBlocks; height++ {
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
			_, _, err := slowPendingBlocks.HandlePart(height, 0, recoveryPart, proof)
			require.NoError(t, err)
		}
	}

	// Slow node should now have all remaining blocks
	slowDelivered := []*CompletedBlock{{Height: 1}} // Already got block 1
	timeout = time.After(2 * time.Second)
	for len(slowDelivered) < numBlocks {
		select {
		case block := <-slowDelivery.BlockChan():
			slowDelivered = append(slowDelivered, block)
		case <-timeout:
			t.Fatalf("slow node timeout for remaining, got %d/%d", len(slowDelivered), numBlocks)
		}
	}

	// Verify both got all blocks
	require.Len(t, fastDelivered, numBlocks)
	require.Len(t, slowDelivered, numBlocks)
}

// ============================================================================
// Phase 9.3: Blocksync + Live Consensus Integration Tests
// ============================================================================

// TestBlocksyncWithLiveConsensus verifies that a lagging node can:
// 1. Catch up using blocksync while the network continues producing blocks
// 2. Transition from blocksync mode to live consensus mode
// 3. Receive new blocks via the live partChan mechanism
//
// This tests the critical transition from blocksync to live consensus.
func TestBlocksyncWithLiveConsensus(t *testing.T) {
	p2pCfg := defaultTestP2PConf()
	cleanup, _, sm := state.SetupTestCase(t)
	t.Cleanup(func() {
		cleanup(t)
	})

	reactors, _ := createTestReactors(2, p2pCfg, false, "")
	ahead := reactors[0]
	lagging := reactors[1]

	// Network has produced 5 blocks, lagging node is at height 0
	const initialBlocks = 5

	blocks := make(map[int64]*testBlockData, initialBlocks+5)

	// Create initial blocks on ahead node
	for height := int64(1); height <= initialBlocks; height++ {
		prop, ps, block, metaData := createTestProposal(t, sm, height, 0, 2, 100000)
		cb, parity := createCompactBlock(t, prop, ps, metaData)

		added := ahead.AddProposal(cb)
		require.True(t, added)

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

		ahead.pmtx.Lock()
		ahead.height = height + 1
		ahead.pmtx.Unlock()
	}

	logger := log.TestingLogger()

	// Setup lagging node with dynamic mock that simulates headersync progress
	// Initially, headersync is NOT caught up (caughtUpThreshold > currentHeight)
	// This simulates the blocksync phase where we're still behind
	mockHS := &dynamicMockHeaderSyncReader{
		blocks:            blocks,
		currentHeight:     initialBlocks,
		maxPeerHeight:     initialBlocks,
		caughtUpThreshold: initialBlocks + 1, // Will be caught up when we reach height 6
	}

	pendingBlocks := NewPendingBlocksManager(logger.With("node", "lagging"), nil, PendingBlocksConfig{
		MaxConcurrent: 100,
		MemoryBudget:  1 << 30,
	})
	pendingBlocks.SetHeaderSyncReader(mockHS)
	pendingBlocks.SetLastCommittedHeight(0)

	blockDelivery := NewBlockDeliveryManager(pendingBlocks, 1, logger.With("node", "lagging"))
	blockDelivery.Start()
	defer blockDelivery.Stop()

	lagging.pendingBlocks = pendingBlocks
	lagging.hsReader = mockHS
	lagging.started.Store(true)

	// Lagging node fills capacity (blocksync mode)
	fetched := pendingBlocks.TryFillCapacity()
	require.Equal(t, initialBlocks, fetched, "should fetch all initial headers")

	// Verify lagging is NOT caught up (still doing blocksync)
	require.False(t, lagging.IsCaughtUp(), "should not be caught up initially")

	// Complete blocksync blocks 1-4 (leaving block 5)
	for height := int64(1); height <= initialBlocks-1; height++ {
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

	// Receive blocksync blocks 1-4
	deliveredBlocks := make([]*CompletedBlock, 0, initialBlocks-1)
	timeout := time.After(5 * time.Second)
	for len(deliveredBlocks) < initialBlocks-1 {
		select {
		case block := <-blockDelivery.BlockChan():
			deliveredBlocks = append(deliveredBlocks, block)
			// Simulate consensus committing the block
			pendingBlocks.Prune(block.Height)
			pendingBlocks.SetLastCommittedHeight(block.Height)
		case <-timeout:
			t.Fatalf("timeout waiting for blocksync blocks, got %d/%d", len(deliveredBlocks), initialBlocks-1)
		}
	}

	// Verify blocks 1-4 delivered in order
	for i, block := range deliveredBlocks {
		require.Equal(t, int64(i+1), block.Height)
	}

	// Now complete block 5 (last blocksync block)
	bd5 := blocks[5]
	for i := uint32(0); i < bd5.ps.Total(); i++ {
		part := bd5.ps.GetPart(int(i))
		proof := bd5.cb.GetProof(i)
		recoveryPart := &proptypes.RecoveryPart{
			Height: 5,
			Round:  0,
			Index:  i,
			Data:   part.Bytes.Bytes(),
		}
		_, _, err := pendingBlocks.HandlePart(5, 0, recoveryPart, proof)
		require.NoError(t, err)
	}

	// Receive block 5
	select {
	case block := <-blockDelivery.BlockChan():
		require.Equal(t, int64(5), block.Height)
		pendingBlocks.Prune(block.Height)
		pendingBlocks.SetLastCommittedHeight(block.Height)
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for block 5")
	}

	// Set lagging node's consensus height to 6 (transitioning to live consensus)
	lagging.pmtx.Lock()
	lagging.height = 6
	lagging.pmtx.Unlock()

	// ====== TRANSITION TO LIVE CONSENSUS ======
	// The network produces block 6 - this should flow through partChan (live mode)

	// Create block 6 on ahead node
	prop6, ps6, block6, metaData6 := createTestProposal(t, sm, 6, 0, 2, 100000)
	cb6, parity6 := createCompactBlock(t, prop6, ps6, metaData6)

	added := ahead.AddProposal(cb6)
	require.True(t, added)

	_, parts6, _, has := ahead.getAllState(6, 0, true)
	require.True(t, has)
	parts6.SetProposalData(ps6, parity6)

	blocks[6] = &testBlockData{
		cb:     cb6,
		ps:     ps6,
		parity: parity6,
		block:  block6,
		prop:   prop6,
	}

	ahead.pmtx.Lock()
	ahead.height = 7
	ahead.pmtx.Unlock()

	// Update mock to reflect network progress and that headersync is now caught up
	mockHS.mtx.Lock()
	mockHS.currentHeight = 6
	mockHS.maxPeerHeight = 6
	mockHS.caughtUpThreshold = 6 // Now headersync considers itself caught up
	mockHS.mtx.Unlock()

	// Now the node should be caught up (headersync caught up, no pending blocks below consensus height)
	require.True(t, lagging.IsCaughtUp(), "should be caught up after receiving all blocksync blocks")

	// Add the compact block to lagging node (simulating proposal receipt in live consensus)
	addedToLagging := lagging.AddProposal(cb6)
	require.True(t, addedToLagging, "lagging node should accept live compact block")

	// Verify proposal was added but parts are NOT yet filled in
	// (parts will arrive via handleRecoveryPart)
	_, laggingParts6, _, hasLagging := lagging.getAllState(6, 0, true)
	require.True(t, hasLagging)
	require.False(t, laggingParts6.IsComplete(), "parts should not be complete yet")

	// Send parts for block 6 to lagging node - should go to partChan (live mode)
	partChan := lagging.GetPartChan()
	require.NotNil(t, partChan, "partChan should be available")

	// Create recovery parts and send them through handleRecoveryPart
	// This simulates receiving parts from peers during live consensus
	for i := uint32(0); i < ps6.Total(); i++ {
		part := ps6.GetPart(int(i))
		proof := cb6.GetProof(i)

		recoveryPart := &proptypes.RecoveryPart{
			Height: 6,
			Round:  0,
			Index:  i,
			Data:   part.Bytes.Bytes(),
			Proof:  proof,
		}

		// Process the part through the reactor's handleRecoveryPart
		lagging.handleRecoveryPart(ahead.self, recoveryPart)
	}

	// Verify parts arrived via partChan (live consensus mode)
	receivedParts := make([]types.PartInfo, 0, ps6.Total())
	partsTimeout := time.After(3 * time.Second)

	for len(receivedParts) < int(ps6.Total()) {
		select {
		case partInfo := <-partChan:
			if partInfo.Height == 6 {
				receivedParts = append(receivedParts, partInfo)
			}
		case <-partsTimeout:
			t.Fatalf("timeout waiting for live parts on partChan, got %d/%d parts",
				len(receivedParts), ps6.Total())
		}
	}

	// Verify all parts received for live consensus
	require.Len(t, receivedParts, int(ps6.Total()),
		"should receive all parts via partChan for live consensus")

	// Verify the block did NOT go through blockChan (it's live, not blocksync)
	select {
	case block := <-blockDelivery.BlockChan():
		t.Fatalf("block %d should NOT be delivered via blockChan in live mode", block.Height)
	case <-time.After(500 * time.Millisecond):
		// Expected - no block on blockChan
	}
}

// TestBlocksyncToLiveTransition_NoPendingBlocks verifies the transition from
// blocksync to live consensus when there are no pending blocks left.
func TestBlocksyncToLiveTransition_NoPendingBlocks(t *testing.T) {
	p2pCfg := defaultTestP2PConf()
	cleanup, _, sm := state.SetupTestCase(t)
	t.Cleanup(func() {
		cleanup(t)
	})

	reactors, _ := createTestReactors(2, p2pCfg, false, "")
	ahead := reactors[0]
	lagging := reactors[1]

	const numBlocks = 3

	blocks := make(map[int64]*testBlockData, numBlocks)

	for height := int64(1); height <= numBlocks; height++ {
		prop, ps, block, metaData := createTestProposal(t, sm, height, 0, 2, 100000)
		cb, parity := createCompactBlock(t, prop, ps, metaData)

		added := ahead.AddProposal(cb)
		require.True(t, added)

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

		ahead.pmtx.Lock()
		ahead.height = height + 1
		ahead.pmtx.Unlock()
	}

	logger := log.TestingLogger()

	mockHS := &dynamicMockHeaderSyncReader{
		blocks:            blocks,
		currentHeight:     numBlocks,
		maxPeerHeight:     numBlocks,
		caughtUpThreshold: numBlocks,
	}

	pendingBlocks := NewPendingBlocksManager(logger, nil, PendingBlocksConfig{})
	pendingBlocks.SetHeaderSyncReader(mockHS)
	pendingBlocks.SetLastCommittedHeight(0)

	blockDelivery := NewBlockDeliveryManager(pendingBlocks, 1, logger)
	blockDelivery.Start()
	defer blockDelivery.Stop()

	lagging.pendingBlocks = pendingBlocks
	lagging.hsReader = mockHS
	lagging.started.Store(true)

	// Fill and sync all blocks
	pendingBlocks.TryFillCapacity()

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

	// Receive all blocks
	for height := int64(1); height <= numBlocks; height++ {
		select {
		case block := <-blockDelivery.BlockChan():
			require.Equal(t, height, block.Height)
			pendingBlocks.Prune(block.Height)
			pendingBlocks.SetLastCommittedHeight(block.Height)
		case <-time.After(2 * time.Second):
			t.Fatalf("timeout waiting for block %d", height)
		}
	}

	// Set consensus height to next block
	lagging.pmtx.Lock()
	lagging.height = numBlocks + 1
	lagging.pmtx.Unlock()

	// Verify state after complete sync
	require.Equal(t, 0, pendingBlocks.BlockCount(), "no pending blocks after sync")
	require.True(t, lagging.IsCaughtUp(), "should be caught up")
	require.Equal(t, int64(0), pendingBlocks.LowestHeight(), "no lowest height when empty")
}

// TestBlocksyncWithContinuousBlockProduction verifies that blocksync works
// correctly even when the network continues producing blocks during sync.
func TestBlocksyncWithContinuousBlockProduction(t *testing.T) {
	p2pCfg := defaultTestP2PConf()
	cleanup, _, sm := state.SetupTestCase(t)
	t.Cleanup(func() {
		cleanup(t)
	})

	reactors, _ := createTestReactors(2, p2pCfg, false, "")
	ahead := reactors[0]
	lagging := reactors[1]

	logger := log.TestingLogger()

	// Dynamic blocks map - new blocks added as network progresses
	blocks := make(map[int64]*testBlockData)
	blocksMtx := sync.Mutex{}

	// Helper to create a block
	createBlock := func(height int64) {
		prop, ps, block, metaData := createTestProposal(t, sm, height, 0, 2, 100000)
		cb, parity := createCompactBlock(t, prop, ps, metaData)

		added := ahead.AddProposal(cb)
		require.True(t, added)

		_, parts, _, has := ahead.getAllState(height, 0, true)
		require.True(t, has)
		parts.SetProposalData(ps, parity)

		blocksMtx.Lock()
		blocks[height] = &testBlockData{
			cb:     cb,
			ps:     ps,
			parity: parity,
			block:  block,
			prop:   prop,
		}
		blocksMtx.Unlock()

		ahead.pmtx.Lock()
		ahead.height = height + 1
		ahead.pmtx.Unlock()
	}

	// Create initial 3 blocks
	for height := int64(1); height <= 3; height++ {
		createBlock(height)
	}

	// Setup lagging node with dynamic mock
	mockHS := &dynamicMockHeaderSyncReaderWithMtx{
		blocks:            blocks,
		blocksMtx:         &blocksMtx,
		currentHeight:     3,
		maxPeerHeight:     3,
		caughtUpThreshold: 10, // Won't be caught up until height 10
	}

	pendingBlocks := NewPendingBlocksManager(logger, nil, PendingBlocksConfig{})
	pendingBlocks.SetHeaderSyncReader(mockHS)
	pendingBlocks.SetLastCommittedHeight(0)

	blockDelivery := NewBlockDeliveryManager(pendingBlocks, 1, logger)
	blockDelivery.Start()
	defer blockDelivery.Stop()

	lagging.pendingBlocks = pendingBlocks
	lagging.hsReader = mockHS
	lagging.started.Store(true)

	// Lagging node starts syncing
	fetched := pendingBlocks.TryFillCapacity()
	require.Equal(t, 3, fetched)

	// Sync block 1
	blocksMtx.Lock()
	bd1 := blocks[1]
	blocksMtx.Unlock()
	for i := uint32(0); i < bd1.ps.Total(); i++ {
		part := bd1.ps.GetPart(int(i))
		proof := bd1.cb.GetProof(i)
		recoveryPart := &proptypes.RecoveryPart{Height: 1, Round: 0, Index: i, Data: part.Bytes.Bytes()}
		_, _, err := pendingBlocks.HandlePart(1, 0, recoveryPart, proof)
		require.NoError(t, err)
	}

	select {
	case block := <-blockDelivery.BlockChan():
		require.Equal(t, int64(1), block.Height)
		pendingBlocks.Prune(block.Height)
		pendingBlocks.SetLastCommittedHeight(block.Height)
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for block 1")
	}

	// Network produces blocks 4 and 5 while lagging is syncing
	createBlock(4)
	createBlock(5)

	// Update mock headersync height
	mockHS.mtx.Lock()
	mockHS.currentHeight = 5
	mockHS.maxPeerHeight = 5
	mockHS.mtx.Unlock()

	// Lagging node tries to fill capacity again - should get new headers
	fetched = pendingBlocks.TryFillCapacity()
	require.Equal(t, 2, fetched, "should fetch 2 new headers (4 and 5)")

	// Complete remaining blocks (2, 3, 4, 5)
	for height := int64(2); height <= 5; height++ {
		blocksMtx.Lock()
		bd := blocks[height]
		blocksMtx.Unlock()

		for i := uint32(0); i < bd.ps.Total(); i++ {
			part := bd.ps.GetPart(int(i))
			proof := bd.cb.GetProof(i)
			recoveryPart := &proptypes.RecoveryPart{Height: height, Round: 0, Index: i, Data: part.Bytes.Bytes()}
			_, _, err := pendingBlocks.HandlePart(height, 0, recoveryPart, proof)
			require.NoError(t, err)
		}
	}

	// Receive all remaining blocks in order
	for height := int64(2); height <= 5; height++ {
		select {
		case block := <-blockDelivery.BlockChan():
			require.Equal(t, height, block.Height)
			pendingBlocks.Prune(block.Height)
			pendingBlocks.SetLastCommittedHeight(block.Height)
		case <-time.After(2 * time.Second):
			t.Fatalf("timeout waiting for block %d", height)
		}
	}

	// Verify state
	require.Equal(t, 0, pendingBlocks.BlockCount())
	require.Equal(t, int64(5), pendingBlocks.GetLastCommittedHeight())
}

// dynamicMockHeaderSyncReader simulates a headersync that progresses over time.
type dynamicMockHeaderSyncReader struct {
	mtx               sync.RWMutex
	blocks            map[int64]*testBlockData
	currentHeight     int64
	maxPeerHeight     int64
	caughtUpThreshold int64 // IsCaughtUp returns true when currentHeight >= this
}

func (m *dynamicMockHeaderSyncReader) GetVerifiedHeader(height int64) (*types.Header, *types.BlockID, bool) {
	m.mtx.RLock()
	defer m.mtx.RUnlock()

	if height > m.currentHeight {
		return nil, nil, false
	}

	bd, exists := m.blocks[height]
	if !exists {
		return nil, nil, false
	}

	header := &types.Header{
		Height:  height,
		ChainID: TestChainID,
	}
	blockID := bd.cb.Proposal.BlockID
	return header, &blockID, true
}

func (m *dynamicMockHeaderSyncReader) GetCommit(height int64) *types.Commit {
	m.mtx.RLock()
	defer m.mtx.RUnlock()

	if height > m.currentHeight {
		return nil
	}
	return &types.Commit{
		Height:  height,
		Round:   0,
		BlockID: types.BlockID{Hash: cmtrand.Bytes(32)},
	}
}

func (m *dynamicMockHeaderSyncReader) Height() int64 {
	m.mtx.RLock()
	defer m.mtx.RUnlock()
	return m.currentHeight
}

func (m *dynamicMockHeaderSyncReader) MaxPeerHeight() int64 {
	m.mtx.RLock()
	defer m.mtx.RUnlock()
	return m.maxPeerHeight
}

func (m *dynamicMockHeaderSyncReader) IsCaughtUp() bool {
	m.mtx.RLock()
	defer m.mtx.RUnlock()
	return m.currentHeight >= m.caughtUpThreshold
}

// dynamicMockHeaderSyncReaderWithMtx is like dynamicMockHeaderSyncReader but uses
// an external mutex for the blocks map (for tests where blocks are added dynamically).
type dynamicMockHeaderSyncReaderWithMtx struct {
	mtx               sync.RWMutex
	blocks            map[int64]*testBlockData
	blocksMtx         *sync.Mutex
	currentHeight     int64
	maxPeerHeight     int64
	caughtUpThreshold int64
}

func (m *dynamicMockHeaderSyncReaderWithMtx) GetVerifiedHeader(height int64) (*types.Header, *types.BlockID, bool) {
	m.mtx.RLock()
	currentHeight := m.currentHeight
	m.mtx.RUnlock()

	if height > currentHeight {
		return nil, nil, false
	}

	m.blocksMtx.Lock()
	bd, exists := m.blocks[height]
	m.blocksMtx.Unlock()

	if !exists {
		return nil, nil, false
	}

	header := &types.Header{
		Height:  height,
		ChainID: TestChainID,
	}
	blockID := bd.cb.Proposal.BlockID
	return header, &blockID, true
}

func (m *dynamicMockHeaderSyncReaderWithMtx) GetCommit(height int64) *types.Commit {
	m.mtx.RLock()
	currentHeight := m.currentHeight
	m.mtx.RUnlock()

	if height > currentHeight {
		return nil
	}
	return &types.Commit{
		Height:  height,
		Round:   0,
		BlockID: types.BlockID{Hash: cmtrand.Bytes(32)},
	}
}

func (m *dynamicMockHeaderSyncReaderWithMtx) Height() int64 {
	m.mtx.RLock()
	defer m.mtx.RUnlock()
	return m.currentHeight
}

func (m *dynamicMockHeaderSyncReaderWithMtx) MaxPeerHeight() int64 {
	m.mtx.RLock()
	defer m.mtx.RUnlock()
	return m.maxPeerHeight
}

func (m *dynamicMockHeaderSyncReaderWithMtx) IsCaughtUp() bool {
	m.mtx.RLock()
	defer m.mtx.RUnlock()
	return m.currentHeight >= m.caughtUpThreshold
}

// ============================================================================
// Phase 9.4: Error Recovery Tests
// ============================================================================

// TestBlocksyncErrorRecovery_PeerDisconnect verifies that blocksync continues
// correctly when a peer disconnects mid-sync. The node should:
// 1. Continue downloading parts from remaining peers
// 2. Complete sync without errors
func TestBlocksyncErrorRecovery_PeerDisconnect(t *testing.T) {
	p2pCfg := defaultTestP2PConf()
	cleanup, _, sm := state.SetupTestCase(t)
	t.Cleanup(func() {
		cleanup(t)
	})

	// Create 3 reactors: 2 ahead nodes (peers), 1 lagging node
	reactors, _ := createTestReactors(3, p2pCfg, false, "")
	peer1 := reactors[0]
	peer2 := reactors[1]
	lagging := reactors[2]

	const numBlocks = 5

	blocks := make(map[int64]*testBlockData, numBlocks)

	// Create blocks on both peer nodes
	for height := int64(1); height <= numBlocks; height++ {
		prop, ps, block, metaData := createTestProposal(t, sm, height, 0, 2, 100000)
		cb, parity := createCompactBlock(t, prop, ps, metaData)

		// Add to both peers
		added1 := peer1.AddProposal(cb)
		require.True(t, added1)
		added2 := peer2.AddProposal(cb)
		require.True(t, added2)

		_, parts1, _, has1 := peer1.getAllState(height, 0, true)
		require.True(t, has1)
		parts1.SetProposalData(ps, parity)

		_, parts2, _, has2 := peer2.getAllState(height, 0, true)
		require.True(t, has2)
		parts2.SetProposalData(ps, parity)

		blocks[height] = &testBlockData{
			cb:     cb,
			ps:     ps,
			parity: parity,
			block:  block,
			prop:   prop,
		}

		peer1.pmtx.Lock()
		peer1.height = height + 1
		peer1.pmtx.Unlock()

		peer2.pmtx.Lock()
		peer2.height = height + 1
		peer2.pmtx.Unlock()
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

	lagging.pendingBlocks = pendingBlocks
	lagging.hsReader = mockHS

	pendingBlocks.TryFillCapacity()

	// Receive some parts from peer1 for blocks 1-3
	for height := int64(1); height <= 3; height++ {
		bd := blocks[height]
		// Only get half the parts from peer1
		for i := uint32(0); i < bd.ps.Total()/2; i++ {
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

	// Simulate peer1 disconnect - lagging node removes it from peer list
	lagging.mtx.Lock()
	delete(lagging.peerstate, peer1.self)
	lagging.mtx.Unlock()

	// Now peer2 provides the remaining parts
	for height := int64(1); height <= numBlocks; height++ {
		bd := blocks[height]
		// Get all parts from peer2 (some are duplicates, which should be handled gracefully)
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

	// Verify all blocks are delivered despite peer1 disconnect
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

	// Verify order
	for i, block := range deliveredBlocks {
		require.Equal(t, int64(i+1), block.Height)
	}
}

// TestBlocksyncErrorRecovery_InvalidParts verifies that invalid parts are rejected
// and don't corrupt the block state.
func TestBlocksyncErrorRecovery_InvalidParts(t *testing.T) {
	p2pCfg := defaultTestP2PConf()
	cleanup, _, sm := state.SetupTestCase(t)
	t.Cleanup(func() {
		cleanup(t)
	})

	reactors, _ := createTestReactors(2, p2pCfg, false, "")
	ahead := reactors[0]
	lagging := reactors[1]

	const numBlocks = 2

	blocks := make(map[int64]*testBlockData, numBlocks)

	for height := int64(1); height <= numBlocks; height++ {
		prop, ps, block, metaData := createTestProposal(t, sm, height, 0, 2, 100000)
		cb, parity := createCompactBlock(t, prop, ps, metaData)

		added := ahead.AddProposal(cb)
		require.True(t, added)

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

		ahead.pmtx.Lock()
		ahead.height = height + 1
		ahead.pmtx.Unlock()
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

	lagging.pendingBlocks = pendingBlocks
	lagging.hsReader = mockHS

	pendingBlocks.TryFillCapacity()

	bd1 := blocks[1]

	// Test 1: Part with corrupted data
	corruptedPart := &proptypes.RecoveryPart{
		Height: 1,
		Round:  0,
		Index:  0,
		Data:   cmtrand.Bytes(len(bd1.ps.GetPart(0).Bytes.Bytes())), // Random data
	}
	proof := bd1.cb.GetProof(0)
	added, _, _ := pendingBlocks.HandlePart(1, 0, corruptedPart, proof)
	// The part should be rejected because the hash won't match the proof
	require.False(t, added, "corrupted part should not be added")

	// Verify the block is still active (not corrupted)
	pb := pendingBlocks.GetBlock(1)
	require.NotNil(t, pb)
	require.Equal(t, BlockStateActive, pb.State, "block state should still be active")

	// Test 2: Part with wrong height
	wrongHeightPart := &proptypes.RecoveryPart{
		Height: 999, // Height we're not tracking
		Round:  0,
		Index:  0,
		Data:   bd1.ps.GetPart(0).Bytes.Bytes(),
	}
	var err error
	added, _, err = pendingBlocks.HandlePart(999, 0, wrongHeightPart, proof)
	require.False(t, added, "part for unknown height should not be added")
	_ = err // Error is expected but not required

	// Test 3: Part with out-of-range index
	outOfRangePart := &proptypes.RecoveryPart{
		Height: 1,
		Round:  0,
		Index:  9999, // Way beyond valid range
		Data:   bd1.ps.GetPart(0).Bytes.Bytes(),
	}
	added, _, err = pendingBlocks.HandlePart(1, 0, outOfRangePart, proof)
	require.False(t, added, "part with out-of-range index should not be added")
	_ = err

	// Now send valid parts and verify the block can still complete
	for i := uint32(0); i < bd1.ps.Total(); i++ {
		part := bd1.ps.GetPart(int(i))
		proof := bd1.cb.GetProof(i)
		recoveryPart := &proptypes.RecoveryPart{
			Height: 1,
			Round:  0,
			Index:  i,
			Data:   part.Bytes.Bytes(),
		}
		added, _, err := pendingBlocks.HandlePart(1, 0, recoveryPart, proof)
		require.NoError(t, err)
		require.True(t, added, "valid part %d should be added", i)
	}

	// Block 1 should complete successfully
	select {
	case block := <-blockDelivery.BlockChan():
		require.Equal(t, int64(1), block.Height)
		require.True(t, block.Parts.IsComplete())
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for block 1")
	}
}

// TestBlocksyncErrorRecovery_DuplicateParts verifies that duplicate parts
// are handled gracefully without errors or state corruption.
func TestBlocksyncErrorRecovery_DuplicateParts(t *testing.T) {
	p2pCfg := defaultTestP2PConf()
	cleanup, _, sm := state.SetupTestCase(t)
	t.Cleanup(func() {
		cleanup(t)
	})

	reactors, _ := createTestReactors(1, p2pCfg, false, "")
	_ = reactors[0]

	const numBlocks = 2

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

	bd1 := blocks[1]

	// Send the first part
	part0 := bd1.ps.GetPart(0)
	proof0 := bd1.cb.GetProof(0)
	recoveryPart0 := &proptypes.RecoveryPart{
		Height: 1,
		Round:  0,
		Index:  0,
		Data:   part0.Bytes.Bytes(),
	}

	added, _, err := pendingBlocks.HandlePart(1, 0, recoveryPart0, proof0)
	require.NoError(t, err)
	require.True(t, added, "first send should add part")

	// Send the same part again (duplicate)
	added, _, err = pendingBlocks.HandlePart(1, 0, recoveryPart0, proof0)
	require.NoError(t, err)
	require.False(t, added, "duplicate part should not be added again")

	// Send it a third time
	added, _, err = pendingBlocks.HandlePart(1, 0, recoveryPart0, proof0)
	require.NoError(t, err)
	require.False(t, added, "third send should also return added=false")

	// Verify part count is still 1
	pb := pendingBlocks.GetBlock(1)
	require.NotNil(t, pb)
	partsReceived := 0
	for i := uint32(0); i < bd1.ps.Total(); i++ {
		if pb.Parts.Original().HasPart(int(i)) {
			partsReceived++
		}
	}
	require.Equal(t, 1, partsReceived, "should only have 1 part despite duplicates")

	// Complete the block normally
	for i := uint32(1); i < bd1.ps.Total(); i++ {
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

	select {
	case block := <-blockDelivery.BlockChan():
		require.Equal(t, int64(1), block.Height)
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for block 1")
	}
}

// TestBlocksyncErrorRecovery_StaleHeightParts verifies that parts for already
// committed (pruned) heights are ignored gracefully.
func TestBlocksyncErrorRecovery_StaleHeightParts(t *testing.T) {
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

	// Complete and receive blocks 1 and 2
	for height := int64(1); height <= 2; height++ {
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

	// Receive both blocks
	for height := int64(1); height <= 2; height++ {
		select {
		case block := <-blockDelivery.BlockChan():
			require.Equal(t, height, block.Height)
			pendingBlocks.Prune(block.Height)
			pendingBlocks.SetLastCommittedHeight(block.Height)
		case <-time.After(2 * time.Second):
			t.Fatalf("timeout waiting for block %d", height)
		}
	}

	// Verify heights 1 and 2 are pruned
	require.False(t, pendingBlocks.HasHeight(1))
	require.False(t, pendingBlocks.HasHeight(2))
	require.True(t, pendingBlocks.HasHeight(3))

	// Now try to send parts for already-pruned heights
	bd1 := blocks[1]
	part := bd1.ps.GetPart(0)
	proof := bd1.cb.GetProof(0)
	stalePart := &proptypes.RecoveryPart{
		Height: 1, // Already pruned
		Round:  0,
		Index:  0,
		Data:   part.Bytes.Bytes(),
	}

	added, _, err := pendingBlocks.HandlePart(1, 0, stalePart, proof)
	require.NoError(t, err) // Should not error
	require.False(t, added, "part for pruned height should not be added")

	// Verify block 3 can still complete
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

	select {
	case block := <-blockDelivery.BlockChan():
		require.Equal(t, int64(3), block.Height)
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for block 3")
	}
}

// TestBlocksyncErrorRecovery_MultipleSimultaneousFailures verifies that the
// system handles multiple error conditions simultaneously.
func TestBlocksyncErrorRecovery_MultipleSimultaneousFailures(t *testing.T) {
	p2pCfg := defaultTestP2PConf()
	cleanup, _, sm := state.SetupTestCase(t)
	t.Cleanup(func() {
		cleanup(t)
	})

	reactors, _ := createTestReactors(4, p2pCfg, false, "")
	peer1 := reactors[0]
	peer2 := reactors[1]
	peer3 := reactors[2]
	lagging := reactors[3]

	const numBlocks = 5

	blocks := make(map[int64]*testBlockData, numBlocks)

	for height := int64(1); height <= numBlocks; height++ {
		prop, ps, block, metaData := createTestProposal(t, sm, height, 0, 2, 100000)
		cb, parity := createCompactBlock(t, prop, ps, metaData)

		// Add to all peers
		for _, peer := range []*Reactor{peer1, peer2, peer3} {
			added := peer.AddProposal(cb)
			require.True(t, added)
			_, parts, _, has := peer.getAllState(height, 0, true)
			require.True(t, has)
			parts.SetProposalData(ps, parity)
			peer.pmtx.Lock()
			peer.height = height + 1
			peer.pmtx.Unlock()
		}

		blocks[height] = &testBlockData{
			cb:     cb,
			ps:     ps,
			parity: parity,
			block:  block,
			prop:   prop,
		}
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

	lagging.pendingBlocks = pendingBlocks
	lagging.hsReader = mockHS

	pendingBlocks.TryFillCapacity()

	// Simulate chaotic part reception:
	// - peer1 sends some valid parts
	// - peer2 sends duplicates and some invalid parts
	// - peer3 completes what's missing
	// All interspersed

	for height := int64(1); height <= numBlocks; height++ {
		bd := blocks[height]

		for i := uint32(0); i < bd.ps.Total(); i++ {
			part := bd.ps.GetPart(int(i))
			proof := bd.cb.GetProof(i)

			// peer1: send half the parts
			if i%2 == 0 {
				validPart := &proptypes.RecoveryPart{
					Height: height,
					Round:  0,
					Index:  i,
					Data:   part.Bytes.Bytes(),
				}
				_, _, _ = pendingBlocks.HandlePart(height, 0, validPart, proof)
			}

			// peer2: send duplicates of what peer1 sent
			if i%2 == 0 {
				duplicatePart := &proptypes.RecoveryPart{
					Height: height,
					Round:  0,
					Index:  i,
					Data:   part.Bytes.Bytes(),
				}
				_, _, _ = pendingBlocks.HandlePart(height, 0, duplicatePart, proof)
			}

			// peer2: also try to send some corrupted parts
			if i%3 == 0 {
				corruptedPart := &proptypes.RecoveryPart{
					Height: height,
					Round:  0,
					Index:  i,
					Data:   cmtrand.Bytes(len(part.Bytes.Bytes())), // Corrupted
				}
				_, _, _ = pendingBlocks.HandlePart(height, 0, corruptedPart, proof) // Should be rejected
			}

			// peer3: complete the remaining parts
			if i%2 == 1 {
				validPart := &proptypes.RecoveryPart{
					Height: height,
					Round:  0,
					Index:  i,
					Data:   part.Bytes.Bytes(),
				}
				_, _, _ = pendingBlocks.HandlePart(height, 0, validPart, proof)
			}
		}
	}

	// Despite all the chaos, all blocks should be delivered correctly
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

	// Verify all blocks received in order
	for i, block := range deliveredBlocks {
		require.Equal(t, int64(i+1), block.Height)
		require.True(t, block.Parts.IsComplete())
	}
}

// TestMultiNodeParallelBlocksync_SharedParts verifies that the same parts
// can be shared across multiple nodes (simulating peer-to-peer gossip).
func TestMultiNodeParallelBlocksync_SharedParts(t *testing.T) {
	p2pCfg := defaultTestP2PConf()
	cleanup, _, sm := state.SetupTestCase(t)
	t.Cleanup(func() {
		cleanup(t)
	})

	// Create 3 lagging nodes that share parts
	reactors, _ := createTestReactors(4, p2pCfg, false, "")
	ahead := reactors[0]
	node1 := reactors[1]
	node2 := reactors[2]
	node3 := reactors[3]

	const numBlocks = 3

	blocks := make(map[int64]*testBlockData, numBlocks)

	for height := int64(1); height <= numBlocks; height++ {
		prop, ps, block, metaData := createTestProposal(t, sm, height, 0, 2, 100000)
		cb, parity := createCompactBlock(t, prop, ps, metaData)

		added := ahead.AddProposal(cb)
		require.True(t, added)

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

		ahead.pmtx.Lock()
		ahead.height = height + 1
		ahead.pmtx.Unlock()
	}

	logger := log.TestingLogger()

	// Setup all three lagging nodes
	type laggingNode struct {
		pendingBlocks *PendingBlocksManager
		delivery      *BlockDeliveryManager
	}

	laggingNodes := make([]*laggingNode, 3)
	for i, reactor := range []*Reactor{node1, node2, node3} {
		pb := NewPendingBlocksManager(logger.With("node", i), nil, PendingBlocksConfig{})
		mockHS := &mockHeaderSyncReaderWithBlocks{
			blocks:   blocks,
			height:   numBlocks,
			caughtUp: true,
		}
		pb.SetHeaderSyncReader(mockHS)
		pb.SetLastCommittedHeight(0)
		delivery := NewBlockDeliveryManager(pb, 1, logger.With("node", i))
		delivery.Start()
		defer delivery.Stop()
		reactor.pendingBlocks = pb
		reactor.hsReader = mockHS
		pb.TryFillCapacity()

		laggingNodes[i] = &laggingNode{
			pendingBlocks: pb,
			delivery:      delivery,
		}
	}

	// Distribute parts across nodes: each node gets 1/3 of parts initially
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

			// Each node gets parts where i%3 == nodeIndex
			nodeIdx := int(i) % 3
			_, _, err := laggingNodes[nodeIdx].pendingBlocks.HandlePart(height, 0, recoveryPart, proof)
			require.NoError(t, err)
		}
	}

	// Now "gossip" - each node shares its parts with others
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

			// Share with all nodes (simulating gossip)
			for _, ln := range laggingNodes {
				// HandlePart is idempotent - it won't add duplicates
				_, _, err := ln.pendingBlocks.HandlePart(height, 0, recoveryPart, proof)
				require.NoError(t, err)
			}
		}
	}

	// Wait for all nodes to receive all blocks
	timeout := time.After(5 * time.Second)
	delivered := make([][]*CompletedBlock, 3)
	for i := range delivered {
		delivered[i] = make([]*CompletedBlock, 0, numBlocks)
	}

	allDone := func() bool {
		for _, d := range delivered {
			if len(d) < numBlocks {
				return false
			}
		}
		return true
	}

	for !allDone() {
		select {
		case block := <-laggingNodes[0].delivery.BlockChan():
			delivered[0] = append(delivered[0], block)
		case block := <-laggingNodes[1].delivery.BlockChan():
			delivered[1] = append(delivered[1], block)
		case block := <-laggingNodes[2].delivery.BlockChan():
			delivered[2] = append(delivered[2], block)
		case <-timeout:
			t.Fatalf("timeout: nodes got %d, %d, %d blocks",
				len(delivered[0]), len(delivered[1]), len(delivered[2]))
		}
	}

	// Verify all nodes received blocks in order
	for nodeIdx, nodeDelivered := range delivered {
		for blockIdx, block := range nodeDelivered {
			require.Equal(t, int64(blockIdx+1), block.Height,
				"node %d: block %d should have height %d", nodeIdx, blockIdx, blockIdx+1)
		}
	}
}
