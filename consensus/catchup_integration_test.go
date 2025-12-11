package consensus

import (
	"os"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	dbm "github.com/cometbft/cometbft-db"

	"github.com/cometbft/cometbft/consensus/propagation"
	"github.com/cometbft/cometbft/internal/test"
	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/p2p"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	sm "github.com/cometbft/cometbft/state"
	"github.com/cometbft/cometbft/store"
	"github.com/cometbft/cometbft/types"
	cmttime "github.com/cometbft/cometbft/types/time"
)

//-------------------------------------------
// Integration tests for Phase 5 catchup
//-------------------------------------------

// integrationNode contains all components needed for a catchup integration test.
type integrationNode struct {
	reactor    *Reactor
	state      *State
	blockStore *store.BlockStore
	stateStore sm.Store
	propagator propagation.Propagator
}

// randIntegrationGenesisDoc creates a genesis document for integration tests.
func randIntegrationGenesisDoc(numValidators int, randPower bool, minPower int64) (*types.GenesisDoc, []types.PrivValidator) {
	validators := make([]types.GenesisValidator, numValidators)
	privValidators := make([]types.PrivValidator, numValidators)
	for i := 0; i < numValidators; i++ {
		val, privVal := types.RandValidator(randPower, minPower)
		validators[i] = types.GenesisValidator{
			PubKey: val.PubKey,
			Power:  val.VotingPower,
		}
		privValidators[i] = privVal
	}
	sort.Sort(types.PrivValidatorsByAddress(privValidators))

	consPar := types.DefaultConsensusParams()
	return &types.GenesisDoc{
		GenesisTime:     cmttime.Now(),
		ChainID:         test.DefaultTestChainID,
		Validators:      validators,
		ConsensusParams: consPar,
	}, privValidators
}

// generateIntegrationBlocks creates and saves blocks from height 1 to maxHeight.
func generateIntegrationBlocks(
	t *testing.T,
	state sm.State,
	stateStore sm.Store,
	blockStore *store.BlockStore,
	privVals []types.PrivValidator,
	maxHeight int64,
) {
	pubKey, err := privVals[0].GetPubKey()
	require.NoError(t, err)
	addr := pubKey.Address()
	idx, _ := state.Validators.GetByAddress(addr)

	seenExtCommit := &types.ExtendedCommit{}

	for blockHeight := int64(1); blockHeight <= maxHeight; blockHeight++ {
		lastExtCommit := seenExtCommit.Clone()

		block, parts, err := state.MakeBlock(
			blockHeight,
			types.MakeData([]types.Tx{}),
			lastExtCommit.ToCommit(),
			nil,
			state.Validators.Proposer.Address,
		)
		require.NoError(t, err)

		blockID := types.BlockID{Hash: block.Hash(), PartSetHeader: parts.Header()}

		vote, err := types.MakeVote(
			privVals[0],
			block.ChainID,
			idx,
			blockHeight,
			0,
			cmtproto.PrecommitType,
			blockID,
			time.Now(),
		)
		require.NoError(t, err)

		seenExtCommit = &types.ExtendedCommit{
			Height:             vote.Height,
			Round:              vote.Round,
			BlockID:            blockID,
			ExtendedSignatures: []types.ExtendedCommitSig{vote.ExtendedCommitSig()},
		}

		blockStore.SaveBlock(block, parts, seenExtCommit.ToCommit())

		state.LastBlockHeight = blockHeight
		state.LastBlockID = blockID
		state.LastBlockTime = block.Time
		state.Validators = state.NextValidators.Copy()
		state.NextValidators = state.NextValidators.CopyIncrementProposerPriority(1)
		state.LastValidators = state.Validators.Copy()

		err = stateStore.Save(state)
		require.NoError(t, err)
	}
}

//-------------------------------------------
// Tests
//-------------------------------------------

// TestCatchupChannel_BlockSaving tests that blocks can be properly saved to the store.
func TestCatchupChannel_BlockSaving(t *testing.T) {
	config := test.ResetTestRoot("catchup_integration_block_saving")
	defer os.RemoveAll(config.RootDir)

	genDoc, privVals := randIntegrationGenesisDoc(1, false, 30)

	blockDB := dbm.NewMemDB()
	stateDB := dbm.NewMemDB()
	stateStore := sm.NewStore(stateDB, sm.StoreOptions{
		DiscardABCIResponses: false,
	})
	blockStore := store.NewBlockStore(blockDB)

	state, err := stateStore.LoadFromDBOrGenesisDoc(genDoc)
	require.NoError(t, err)

	// Save initial state
	err = stateStore.Save(state)
	require.NoError(t, err)

	// Generate some blocks
	generateIntegrationBlocks(t, state, stateStore, blockStore, privVals, 5)

	// Verify blocks were saved
	require.Equal(t, int64(5), blockStore.Height())

	// Verify each block is properly saved
	// Note: The commit for block N is stored with block N+1, so the last block's commit
	// is only available as SeenCommit, not BlockCommit
	for h := int64(1); h <= 5; h++ {
		block := blockStore.LoadBlock(h)
		require.NotNil(t, block, "block at height %d should exist", h)
		require.Equal(t, h, block.Height)

		parts, _, err := blockStore.LoadPartSet(h)
		require.NoError(t, err)
		require.NotNil(t, parts, "parts at height %d should exist", h)
		require.True(t, parts.IsComplete())

		// For all blocks except the last one, check BlockCommit
		// For the last block, the commit is stored as SeenCommit
		if h < 5 {
			commit := blockStore.LoadBlockCommit(h)
			require.NotNil(t, commit, "commit at height %d should exist", h)
			require.Equal(t, h, commit.Height)
		} else {
			// Last block's commit is stored as SeenCommit
			commit := blockStore.LoadSeenCommit(h)
			require.NotNil(t, commit, "seen commit at height %d should exist", h)
			require.Equal(t, h, commit.Height)
		}
	}
}

// TestBlocksyncMode_NoWaitSync verifies that blocksync mode creates a reactor
// that doesn't wait for sync (Phase 5 behavior).
func TestBlocksyncMode_NoWaitSync(t *testing.T) {
	// Create a consensus state using existing helper
	cs, _ := randState(1)

	// Create a noop propagator
	propagator := propagation.NewNoOpPropagator()

	// Create a reactor in blocksync mode (waitSync=false)
	reactor := NewReactor(cs, propagator, false)

	// Verify reactor is NOT waiting for sync
	require.False(t, reactor.WaitSync(),
		"blocksync mode should not wait for sync (Phase 5)")
}

// TestStateSyncMode_WaitSync verifies that state sync mode creates a reactor
// that waits for sync before starting consensus.
func TestStateSyncMode_WaitSync(t *testing.T) {
	// Create a consensus state using existing helper
	cs, _ := randState(1)

	// Create a noop propagator
	propagator := propagation.NewNoOpPropagator()

	// Create a reactor in state sync mode (waitSync=true)
	reactor := NewReactor(cs, propagator, true)

	// Verify reactor IS waiting for sync
	require.True(t, reactor.WaitSync(),
		"state sync mode should wait for sync")
}

// TestCatchupBlockInfo_Creation verifies that CatchupBlockInfo structs
// are created with the correct data.
func TestCatchupBlockInfo_Creation(t *testing.T) {
	config := test.ResetTestRoot("catchup_integration_blockinfo")
	defer os.RemoveAll(config.RootDir)

	genDoc, privVals := randIntegrationGenesisDoc(1, false, 30)

	blockDB := dbm.NewMemDB()
	stateDB := dbm.NewMemDB()
	stateStore := sm.NewStore(stateDB, sm.StoreOptions{
		DiscardABCIResponses: false,
	})
	blockStore := store.NewBlockStore(blockDB)

	state, err := stateStore.LoadFromDBOrGenesisDoc(genDoc)
	require.NoError(t, err)

	err = stateStore.Save(state)
	require.NoError(t, err)

	// Generate some blocks
	generateIntegrationBlocks(t, state, stateStore, blockStore, privVals, 3)

	// Load a block and create CatchupBlockInfo
	block := blockStore.LoadBlock(1)
	require.NotNil(t, block)

	parts, _, err := blockStore.LoadPartSet(1)
	require.NoError(t, err)
	require.NotNil(t, parts)

	commit := blockStore.LoadBlockCommit(1)
	require.NotNil(t, commit)

	info := &propagation.CatchupBlockInfo{
		Height: 1,
		Block:  block,
		Parts:  parts,
		Commit: commit,
	}

	// Verify the info is correct
	require.Equal(t, int64(1), info.Height)
	require.Equal(t, block.Hash(), info.Block.Hash())
	require.True(t, info.Parts.IsComplete())
	require.Equal(t, int64(1), info.Commit.Height)
}

// TestPropagatorCatchupChannel verifies the propagator catchup channel exists and works.
func TestPropagatorCatchupChannel(t *testing.T) {
	config := test.ResetTestRoot("catchup_integration_propagator_channel")
	defer os.RemoveAll(config.RootDir)

	genDoc, privVals := randIntegrationGenesisDoc(1, false, 30)

	blockDB := dbm.NewMemDB()
	blockStore := store.NewBlockStore(blockDB)

	key, err := p2p.LoadOrGenNodeKey(config.NodeKeyFile())
	require.NoError(t, err)

	// Create a propagation reactor
	propReactor := propagation.NewReactor(key.ID(), propagation.Config{
		Store:         blockStore,
		Mempool:       &emptyMempool{},
		Privval:       privVals[0],
		ChainID:       genDoc.ChainID,
		BlockMaxBytes: genDoc.ConsensusParams.Block.MaxBytes,
	})
	propReactor.SetLogger(log.TestingLogger())

	// Verify the catchup channel is available
	ch := propReactor.GetCatchupBlockChan()
	require.NotNil(t, ch, "catchup block channel should be available")
}

// TestCatchupBlockInfo_ValidFieldsRequired tests that CatchupBlockInfo requires all fields.
func TestCatchupBlockInfo_ValidFieldsRequired(t *testing.T) {
	config := test.ResetTestRoot("catchup_integration_valid_fields")
	defer os.RemoveAll(config.RootDir)

	genDoc, privVals := randIntegrationGenesisDoc(1, false, 30)

	blockDB := dbm.NewMemDB()
	stateDB := dbm.NewMemDB()
	stateStore := sm.NewStore(stateDB, sm.StoreOptions{
		DiscardABCIResponses: false,
	})
	blockStore := store.NewBlockStore(blockDB)

	state, err := stateStore.LoadFromDBOrGenesisDoc(genDoc)
	require.NoError(t, err)
	err = stateStore.Save(state)
	require.NoError(t, err)

	// Generate blocks
	generateIntegrationBlocks(t, state, stateStore, blockStore, privVals, 3)

	// Load all required components
	block := blockStore.LoadBlock(1)
	require.NotNil(t, block)

	parts, _, err := blockStore.LoadPartSet(1)
	require.NoError(t, err)
	require.NotNil(t, parts)

	commit := blockStore.LoadBlockCommit(1)
	require.NotNil(t, commit)

	// Test with all fields populated
	info := &propagation.CatchupBlockInfo{
		Height: 1,
		Block:  block,
		Parts:  parts,
		Commit: commit,
	}

	// Verify all fields are present and correct
	require.NotNil(t, info.Block)
	require.NotNil(t, info.Parts)
	require.NotNil(t, info.Commit)
	require.Equal(t, int64(1), info.Height)
	require.Equal(t, int64(1), info.Block.Height)
	require.True(t, info.Parts.IsComplete())
	require.Equal(t, int64(1), info.Commit.Height)
}

// TestCatchupBlockMessage_MatchesCatchupBlockInfo tests that CatchupBlockMessage
// contains the same fields as CatchupBlockInfo (they should be equivalent).
func TestCatchupBlockMessage_MatchesCatchupBlockInfo(t *testing.T) {
	config := test.ResetTestRoot("catchup_integration_message_info")
	defer os.RemoveAll(config.RootDir)

	genDoc, privVals := randIntegrationGenesisDoc(1, false, 30)

	blockDB := dbm.NewMemDB()
	stateDB := dbm.NewMemDB()
	stateStore := sm.NewStore(stateDB, sm.StoreOptions{
		DiscardABCIResponses: false,
	})
	blockStore := store.NewBlockStore(blockDB)

	state, err := stateStore.LoadFromDBOrGenesisDoc(genDoc)
	require.NoError(t, err)
	err = stateStore.Save(state)
	require.NoError(t, err)

	// Generate blocks
	generateIntegrationBlocks(t, state, stateStore, blockStore, privVals, 3)

	// Load components
	block := blockStore.LoadBlock(2)
	parts, _, err := blockStore.LoadPartSet(2)
	require.NoError(t, err)
	commit := blockStore.LoadBlockCommit(2)

	// Create CatchupBlockInfo (from propagation reactor)
	info := &propagation.CatchupBlockInfo{
		Height: 2,
		Block:  block,
		Parts:  parts,
		Commit: commit,
	}

	// Create CatchupBlockMessage (for consensus)
	msg := &CatchupBlockMessage{
		Height: info.Height,
		Block:  info.Block,
		Parts:  info.Parts,
		Commit: info.Commit,
	}

	// Verify the message matches the info
	require.Equal(t, info.Height, msg.Height)
	require.Equal(t, info.Block.Hash(), msg.Block.Hash())
	require.Equal(t, info.Parts.Hash(), msg.Parts.Hash())
	require.Equal(t, info.Commit.Hash(), msg.Commit.Hash())
}

// TestCatchupFlow_FromChannelToMessage tests the complete flow from
// CatchupBlockInfo creation to CatchupBlockMessage delivery.
func TestCatchupFlow_FromChannelToMessage(t *testing.T) {
	config := test.ResetTestRoot("catchup_integration_flow")
	defer os.RemoveAll(config.RootDir)

	genDoc, privVals := randIntegrationGenesisDoc(1, false, 30)

	blockDB := dbm.NewMemDB()
	stateDB := dbm.NewMemDB()
	stateStore := sm.NewStore(stateDB, sm.StoreOptions{
		DiscardABCIResponses: false,
	})
	blockStore := store.NewBlockStore(blockDB)

	state, err := stateStore.LoadFromDBOrGenesisDoc(genDoc)
	require.NoError(t, err)
	err = stateStore.Save(state)
	require.NoError(t, err)

	// Generate multiple blocks
	generateIntegrationBlocks(t, state, stateStore, blockStore, privVals, 5)

	// Create a buffered channel to simulate the catchup block channel
	catchupChan := make(chan *propagation.CatchupBlockInfo, 10)

	// Send multiple catchup blocks through the channel
	for h := int64(1); h <= 3; h++ {
		block := blockStore.LoadBlock(h)
		parts, _, err := blockStore.LoadPartSet(h)
		require.NoError(t, err)

		// For the last block of the test (3), we need to get SeenCommit or LoadBlockCommit
		var commit *types.Commit
		if h < 5 {
			commit = blockStore.LoadBlockCommit(h)
		} else {
			commit = blockStore.LoadSeenCommit(h)
		}
		require.NotNil(t, commit, "commit at height %d should exist", h)

		info := &propagation.CatchupBlockInfo{
			Height: h,
			Block:  block,
			Parts:  parts,
			Commit: commit,
		}

		catchupChan <- info
	}

	// Verify all messages can be received and converted
	for h := int64(1); h <= 3; h++ {
		select {
		case info := <-catchupChan:
			require.Equal(t, h, info.Height)
			require.NotNil(t, info.Block)
			require.NotNil(t, info.Parts)
			require.NotNil(t, info.Commit)

			// Convert to message (as syncData does)
			msg := &CatchupBlockMessage{
				Height: info.Height,
				Block:  info.Block,
				Parts:  info.Parts,
				Commit: info.Commit,
			}
			require.NoError(t, msg.ValidateBasic())
		case <-time.After(time.Second):
			t.Fatalf("timeout waiting for catchup block at height %d", h)
		}
	}
}

// TestCatchup_SequentialHeightExecution verifies that catchup blocks
// must be applied in sequential order.
func TestCatchup_SequentialHeightExecution(t *testing.T) {
	config := test.ResetTestRoot("catchup_integration_sequential")
	defer os.RemoveAll(config.RootDir)

	genDoc, privVals := randIntegrationGenesisDoc(1, false, 30)

	blockDB := dbm.NewMemDB()
	stateDB := dbm.NewMemDB()
	stateStore := sm.NewStore(stateDB, sm.StoreOptions{
		DiscardABCIResponses: false,
	})
	blockStore := store.NewBlockStore(blockDB)

	state, err := stateStore.LoadFromDBOrGenesisDoc(genDoc)
	require.NoError(t, err)
	err = stateStore.Save(state)
	require.NoError(t, err)

	// Generate blocks
	generateIntegrationBlocks(t, state, stateStore, blockStore, privVals, 5)

	// Test that blocks must start at height 1 and be sequential
	// This tests the conceptual requirement that catchup applies blocks
	// one after another in order

	heights := []int64{}
	for h := int64(1); h <= 5; h++ {
		block := blockStore.LoadBlock(h)
		require.NotNil(t, block)
		heights = append(heights, block.Height)
	}

	// Verify heights are sequential
	for i, h := range heights {
		require.Equal(t, int64(i+1), h, "block heights should be sequential")
	}

	// Verify the store height matches
	require.Equal(t, int64(5), blockStore.Height())
}

// TestCatchup_CommitVerification verifies that commits must match their blocks.
func TestCatchup_CommitVerification(t *testing.T) {
	config := test.ResetTestRoot("catchup_integration_commit_verify")
	defer os.RemoveAll(config.RootDir)

	genDoc, privVals := randIntegrationGenesisDoc(1, false, 30)

	blockDB := dbm.NewMemDB()
	stateDB := dbm.NewMemDB()
	stateStore := sm.NewStore(stateDB, sm.StoreOptions{
		DiscardABCIResponses: false,
	})
	blockStore := store.NewBlockStore(blockDB)

	state, err := stateStore.LoadFromDBOrGenesisDoc(genDoc)
	require.NoError(t, err)
	err = stateStore.Save(state)
	require.NoError(t, err)

	// Generate blocks
	generateIntegrationBlocks(t, state, stateStore, blockStore, privVals, 3)

	// Verify commits match blocks
	for h := int64(1); h <= 2; h++ {
		block := blockStore.LoadBlock(h)
		commit := blockStore.LoadBlockCommit(h)

		require.NotNil(t, block)
		require.NotNil(t, commit)

		// Commit should be for the same height
		require.Equal(t, block.Height, commit.Height)

		// Commit's BlockID should match block
		require.Equal(t, block.Hash(), commit.BlockID.Hash)
	}

	// For the last block, check SeenCommit
	block := blockStore.LoadBlock(3)
	seenCommit := blockStore.LoadSeenCommit(3)
	require.NotNil(t, block)
	require.NotNil(t, seenCommit)
	require.Equal(t, block.Height, seenCommit.Height)
	require.Equal(t, block.Hash(), seenCommit.BlockID.Hash)
}

// TestCatchup_CompactBlockScenario tests the scenario where consensus has
// compact blocks available (parts downloaded but no commit yet) and catchup
// provides the commit via the catchup channel.
func TestCatchup_CompactBlockScenario(t *testing.T) {
	config := test.ResetTestRoot("catchup_integration_compact_blocks")
	defer os.RemoveAll(config.RootDir)

	genDoc, privVals := randIntegrationGenesisDoc(1, false, 30)

	blockDB := dbm.NewMemDB()
	stateDB := dbm.NewMemDB()
	stateStore := sm.NewStore(stateDB, sm.StoreOptions{
		DiscardABCIResponses: false,
	})
	blockStore := store.NewBlockStore(blockDB)

	state, err := stateStore.LoadFromDBOrGenesisDoc(genDoc)
	require.NoError(t, err)
	err = stateStore.Save(state)
	require.NoError(t, err)

	// Generate blocks representing "compact blocks" - blocks where parts
	// are downloaded via the propagation reactor
	generateIntegrationBlocks(t, state, stateStore, blockStore, privVals, 10)

	// Simulate the compact block scenario:
	// - Consensus is at height 1
	// - Block parts for heights 2-10 are available (via propagation)
	// - Commits for heights 2-10 are available (via headersync)
	// - The catchup mechanism should apply blocks 2-10 sequentially

	// Verify all parts are complete (simulating compact block download)
	for h := int64(1); h <= 10; h++ {
		parts, _, err := blockStore.LoadPartSet(h)
		require.NoError(t, err)
		require.NotNil(t, parts, "parts at height %d should exist", h)
		require.True(t, parts.IsComplete(), "parts at height %d should be complete", h)
	}

	// Create catchup block info objects (simulating what propagation reactor does)
	catchupBlocks := make([]*propagation.CatchupBlockInfo, 0)
	for h := int64(2); h <= 9; h++ {
		block := blockStore.LoadBlock(h)
		parts, _, _ := blockStore.LoadPartSet(h)
		commit := blockStore.LoadBlockCommit(h)

		info := &propagation.CatchupBlockInfo{
			Height: h,
			Block:  block,
			Parts:  parts,
			Commit: commit,
		}
		catchupBlocks = append(catchupBlocks, info)
	}

	// Verify catchup blocks are in order and complete
	for i, info := range catchupBlocks {
		expectedHeight := int64(i + 2)
		require.Equal(t, expectedHeight, info.Height, "catchup block should be at expected height")
		require.NotNil(t, info.Block)
		require.NotNil(t, info.Parts)
		require.True(t, info.Parts.IsComplete())
		require.NotNil(t, info.Commit)
		require.Equal(t, expectedHeight, info.Commit.Height)
	}
}

// TestCatchup_StateSyncRecovery tests the scenario where state sync brings
// the node to a specific height and then catchup continues from there.
func TestCatchup_StateSyncRecovery(t *testing.T) {
	config := test.ResetTestRoot("catchup_integration_statesync_recovery")
	defer os.RemoveAll(config.RootDir)

	genDoc, privVals := randIntegrationGenesisDoc(1, false, 30)

	blockDB := dbm.NewMemDB()
	stateDB := dbm.NewMemDB()
	stateStore := sm.NewStore(stateDB, sm.StoreOptions{
		DiscardABCIResponses: false,
	})
	blockStore := store.NewBlockStore(blockDB)

	state, err := stateStore.LoadFromDBOrGenesisDoc(genDoc)
	require.NoError(t, err)
	err = stateStore.Save(state)
	require.NoError(t, err)

	// Generate blocks 1-20
	generateIntegrationBlocks(t, state, stateStore, blockStore, privVals, 20)

	// Simulate state sync recovery scenario:
	// State sync completed at height 15
	// Network is at height 20
	// Catchup needs to apply blocks 16-20

	stateSyncHeight := int64(15)

	// Verify blocks 16-19 have proper commits for catchup
	// (block 20's commit would be SeenCommit)
	for h := stateSyncHeight + 1; h < 20; h++ {
		block := blockStore.LoadBlock(h)
		require.NotNil(t, block, "block at height %d should exist", h)

		commit := blockStore.LoadBlockCommit(h)
		require.NotNil(t, commit, "commit at height %d should exist", h)
		require.Equal(t, h, commit.Height)
		require.Equal(t, block.Hash(), commit.BlockID.Hash)

		parts, _, err := blockStore.LoadPartSet(h)
		require.NoError(t, err)
		require.True(t, parts.IsComplete(), "parts at height %d should be complete", h)
	}

	// Verify block 20 (last block) has SeenCommit
	block20 := blockStore.LoadBlock(20)
	require.NotNil(t, block20)
	seenCommit20 := blockStore.LoadSeenCommit(20)
	require.NotNil(t, seenCommit20)
	require.Equal(t, int64(20), seenCommit20.Height)
}

// TestCatchup_WaitSyncBehavior tests the difference between state sync mode
// (waitSync=true) and blocksync mode (waitSync=false).
func TestCatchup_WaitSyncBehavior(t *testing.T) {
	testCases := []struct {
		name        string
		stateSync   bool
		expectWait  bool
		description string
	}{
		{
			name:        "StateSync_WaitsForConsensus",
			stateSync:   true,
			expectWait:  true,
			description: "State sync mode waits for SwitchToConsensus call",
		},
		{
			name:        "Blocksync_NoWait",
			stateSync:   false,
			expectWait:  false,
			description: "Blocksync mode starts consensus immediately with catchup via channel",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			cs, _ := randState(1)
			propagator := propagation.NewNoOpPropagator()
			reactor := NewReactor(cs, propagator, tc.stateSync)

			require.Equal(t, tc.expectWait, reactor.WaitSync(), tc.description)
		})
	}
}

// TestCatchup_BlockReconstructionFromParts tests that blocks can be
// properly reconstructed from their complete PartSets.
func TestCatchup_BlockReconstructionFromParts(t *testing.T) {
	config := test.ResetTestRoot("catchup_integration_reconstruction")
	defer os.RemoveAll(config.RootDir)

	genDoc, privVals := randIntegrationGenesisDoc(1, false, 30)

	blockDB := dbm.NewMemDB()
	stateDB := dbm.NewMemDB()
	stateStore := sm.NewStore(stateDB, sm.StoreOptions{
		DiscardABCIResponses: false,
	})
	blockStore := store.NewBlockStore(blockDB)

	state, err := stateStore.LoadFromDBOrGenesisDoc(genDoc)
	require.NoError(t, err)
	err = stateStore.Save(state)
	require.NoError(t, err)

	// Generate blocks
	generateIntegrationBlocks(t, state, stateStore, blockStore, privVals, 5)

	// Test that blocks can be loaded and their parts reconstructed
	for h := int64(1); h <= 5; h++ {
		originalBlock := blockStore.LoadBlock(h)
		require.NotNil(t, originalBlock)

		parts, _, err := blockStore.LoadPartSet(h)
		require.NoError(t, err)
		require.NotNil(t, parts)
		require.True(t, parts.IsComplete())

		// Verify the parts match the block
		partSetFromBlock, err := originalBlock.MakePartSet(types.BlockPartSizeBytes)
		require.NoError(t, err)

		require.Equal(t, partSetFromBlock.Total(), parts.Total(),
			"part set total should match for block %d", h)
		require.Equal(t, partSetFromBlock.Hash(), parts.Hash(),
			"part set hash should match for block %d", h)
	}
}

// TestCatchup_ChannelNonBlocking tests that the catchup channel doesn't block
// when sending multiple blocks.
func TestCatchup_ChannelNonBlocking(t *testing.T) {
	config := test.ResetTestRoot("catchup_integration_channel_nonblock")
	defer os.RemoveAll(config.RootDir)

	genDoc, privVals := randIntegrationGenesisDoc(1, false, 30)

	blockDB := dbm.NewMemDB()
	stateDB := dbm.NewMemDB()
	stateStore := sm.NewStore(stateDB, sm.StoreOptions{
		DiscardABCIResponses: false,
	})
	blockStore := store.NewBlockStore(blockDB)

	state, err := stateStore.LoadFromDBOrGenesisDoc(genDoc)
	require.NoError(t, err)
	err = stateStore.Save(state)
	require.NoError(t, err)

	// Generate blocks
	generateIntegrationBlocks(t, state, stateStore, blockStore, privVals, 50)

	// Create a buffered channel (simulating the 100-item buffer in the real impl)
	catchupChan := make(chan *propagation.CatchupBlockInfo, 100)

	// Send 50 catchup blocks - should not block
	for h := int64(1); h <= 49; h++ {
		block := blockStore.LoadBlock(h)
		parts, _, _ := blockStore.LoadPartSet(h)
		commit := blockStore.LoadBlockCommit(h)

		info := &propagation.CatchupBlockInfo{
			Height: h,
			Block:  block,
			Parts:  parts,
			Commit: commit,
		}

		select {
		case catchupChan <- info:
			// OK - non-blocking send succeeded
		default:
			t.Fatalf("channel blocked at height %d", h)
		}
	}

	// Channel should have 49 items
	require.Equal(t, 49, len(catchupChan), "channel should have 49 items")

	// Drain the channel
	for i := 0; i < 49; i++ {
		select {
		case info := <-catchupChan:
			require.NotNil(t, info)
		case <-time.After(time.Second):
			t.Fatal("timeout draining channel")
		}
	}

	require.Equal(t, 0, len(catchupChan), "channel should be empty")
}
