package store

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/cosmos/gogoproto/proto"
	"github.com/stretchr/testify/require"

	dbm "github.com/cometbft/cometbft-db"
	"github.com/cometbft/cometbft/internal/test"
	cmtstore "github.com/cometbft/cometbft/proto/tendermint/store"
	sm "github.com/cometbft/cometbft/state"
	"github.com/cometbft/cometbft/types"
	cmttime "github.com/cometbft/cometbft/types/time"
)

// makeStateAndFileBlockStore creates a new state and file block store for testing
func makeStateAndFileBlockStore(t *testing.T) (sm.State, *FileBlockStore, func()) {
	config := test.ResetTestRoot("file_block_store_test")
	stateStore := sm.NewStore(dbm.NewMemDB(), sm.StoreOptions{
		DiscardABCIResponses: false,
	})
	state, err := stateStore.LoadFromDBOrGenesisFile(config.GenesisFile())
	require.NoError(t, err)

	// Create a temporary directory for the file block store
	tempDir, err := os.MkdirTemp("", "file_block_store_test")
	require.NoError(t, err)

	bs, err := NewFileBlockStore(tempDir)
	require.NoError(t, err)

	cleanup := func() {
		os.RemoveAll(config.RootDir)
		os.RemoveAll(tempDir)
	}

	return state, bs, cleanup
}

func TestNewFileBlockStore(t *testing.T) {
	// Create a temporary directory
	tempDir, err := os.MkdirTemp("", "fbs_new_test_*")
	require.NoError(t, err)
	defer func() {
		if t.Failed() {
			t.Logf("TestNewFileBlockStore failed, preserving test directory: %s", tempDir)
			return
		}
		os.RemoveAll(tempDir)
	}()

	// Create a new file store
	bs, err := NewFileBlockStore(tempDir)
	require.NoError(t, err)
	require.NotNil(t, bs)
	require.NoError(t, bs.Close()) // Close the store
}

func TestFileBlockStoreSaveLoadBlock(t *testing.T) {
	state, bs, cleanup := makeStateAndFileBlockStore(t)
	defer cleanup()

	// Initially empty
	require.Equal(t, int64(0), bs.Base())
	require.Equal(t, int64(0), bs.Height())

	// Create and save a block
	block := state.MakeBlock(1, types.MakeData(nil), new(types.Commit), nil, state.Validators.GetProposer().Address)
	partSet, err := block.MakePartSet(types.BlockPartSizeBytes)
	require.NoError(t, err)
	seenCommit := makeTestExtCommit(block.Header.Height, cmttime.Now())
	bs.SaveBlockWithExtendedCommit(block, partSet, seenCommit)

	// Verify block was saved
	require.Equal(t, int64(1), bs.Base())
	require.Equal(t, int64(1), bs.Height())

	// Load the block
	loadedBlock := bs.LoadBlock(1)
	require.NotNil(t, loadedBlock)
	require.Equal(t, block.Hash(), loadedBlock.Hash())

	// Load block meta
	meta := bs.LoadBlockMeta(1)
	require.NotNil(t, meta)
	require.Equal(t, block.Hash(), meta.BlockID.Hash)

	// Load block parts
	part := bs.LoadBlockPart(1, 0)
	require.NotNil(t, part)
	expectedPart := partSet.GetPart(0)
	require.Equal(t, expectedPart.Index, part.Index)
	require.Equal(t, expectedPart.Bytes, part.Bytes)
	require.Equal(t, expectedPart.Proof.Total, part.Proof.Total)
	require.Equal(t, expectedPart.Proof.Index, part.Proof.Index)
	require.Equal(t, expectedPart.Proof.LeafHash, part.Proof.LeafHash)
	// Compare Aunts, ensuring to handle nil slices correctly
	if len(expectedPart.Proof.Aunts) == 0 {
		require.Empty(t, part.Proof.Aunts, "Expected part.Proof.Aunts to be empty if expectedPart.Proof.Aunts is empty")
	} else {
		require.Equal(t, expectedPart.Proof.Aunts, part.Proof.Aunts)
	}

	// Load block commit
	commit := bs.LoadBlockCommit(1)
	require.NotNil(t, commit)
	require.Equal(t, seenCommit.ToCommit().Hash(), commit.Hash())

	// Load extended commit
	extCommit := bs.LoadBlockExtendedCommit(1)
	require.NotNil(t, extCommit)
	require.Equal(t, seenCommit.ToCommit().Hash(), extCommit.ToCommit().Hash())
}

func TestFileBlockStorePruneBlocks(t *testing.T) {
	state, bs, cleanup := makeStateAndFileBlockStore(t)
	defer cleanup()

	// Create and save multiple blocks
	for h := int64(1); h <= 10; h++ {
		block := state.MakeBlock(h, types.MakeData(nil), new(types.Commit), nil, state.Validators.GetProposer().Address)
		partSet, err := block.MakePartSet(types.BlockPartSizeBytes)
		require.NoError(t, err)
		seenCommit := makeTestExtCommit(block.Header.Height, cmttime.Now())
		bs.SaveBlockWithExtendedCommit(block, partSet, seenCommit)
	}

	// Verify initial state
	require.Equal(t, int64(1), bs.Base())
	require.Equal(t, int64(10), bs.Height())
	require.Equal(t, int64(10), bs.Size())

	// Prune blocks up to height 5
	pruned, _, err := bs.PruneBlocks(5, state)
	require.NoError(t, err)
	require.Equal(t, uint64(5), pruned)
	require.Equal(t, int64(6), bs.Base())
	require.Equal(t, int64(10), bs.Height())
	require.Equal(t, int64(5), bs.Size())

	// Verify blocks were pruned
	require.Nil(t, bs.LoadBlock(5))
	require.NotNil(t, bs.LoadBlock(6))
}

func TestFileBlockStoreDeleteLatestBlock(t *testing.T) {
	state, bs, cleanup := makeStateAndFileBlockStore(t)
	defer cleanup()

	// Create and save multiple blocks
	for h := int64(1); h <= 5; h++ {
		block := state.MakeBlock(h, types.MakeData(nil), new(types.Commit), nil, state.Validators.GetProposer().Address)
		partSet, err := block.MakePartSet(types.BlockPartSizeBytes)
		require.NoError(t, err)
		seenCommit := makeTestExtCommit(block.Header.Height, cmttime.Now())
		bs.SaveBlockWithExtendedCommit(block, partSet, seenCommit)
	}

	// Verify initial state
	require.Equal(t, int64(5), bs.Height())

	// Delete latest block
	err := bs.DeleteLatestBlock()
	require.NoError(t, err)
	require.Equal(t, int64(4), bs.Height())

	// Verify block was deleted
	require.Nil(t, bs.LoadBlock(5))
	require.NotNil(t, bs.LoadBlock(4))
}

func TestFileBlockStoreSaveLoadTxInfo(t *testing.T) {
	state, bs, cleanup := makeStateAndFileBlockStore(t)
	defer cleanup()

	// Create a block with transactions
	txs := []types.Tx{[]byte("tx1"), []byte("tx2")}
	block := state.MakeBlock(1, types.MakeData(txs), new(types.Commit), nil, state.Validators.GetProposer().Address)
	partSet, err := block.MakePartSet(types.BlockPartSizeBytes)
	require.NoError(t, err)
	seenCommit := makeTestExtCommit(block.Header.Height, cmttime.Now())
	bs.SaveBlockWithExtendedCommit(block, partSet, seenCommit)

	// Save transaction info
	txResponseCodes := []uint32{0, 1}
	txLogs := []string{"success", "failure"}
	err = bs.SaveTxInfo(block, txResponseCodes, txLogs)
	require.NoError(t, err)

	// Load and verify transaction info
	for i, tx := range txs {
		txInfo := bs.LoadTxInfo(tx.Hash())
		require.NotNil(t, txInfo)
		require.Equal(t, block.Height, txInfo.Height)
		require.Equal(t, uint32(i), txInfo.Index)
		require.Equal(t, txResponseCodes[i], txInfo.Code)
		if txResponseCodes[i] == 0 {
			require.Equal(t, "", txInfo.Error)
		} else {
			require.Equal(t, txLogs[i], txInfo.Error)
		}
	}
}

func TestFileBlockStoreLoadBlockByHash(t *testing.T) {
	state, bs, cleanup := makeStateAndFileBlockStore(t)
	defer cleanup()

	// Create and save a block
	block := state.MakeBlock(1, types.MakeData(nil), new(types.Commit), nil, state.Validators.GetProposer().Address)
	partSet, err := block.MakePartSet(types.BlockPartSizeBytes)
	require.NoError(t, err)
	seenCommit := makeTestExtCommit(block.Header.Height, cmttime.Now())
	bs.SaveBlockWithExtendedCommit(block, partSet, seenCommit)

	// Load block by hash
	loadedBlock := bs.LoadBlockByHash(block.Hash())
	require.NotNil(t, loadedBlock)
	require.Equal(t, block.Hash(), loadedBlock.Hash())
}

func TestFileBlockStoreLoadSeenCommit(t *testing.T) {
	state, bs, cleanup := makeStateAndFileBlockStore(t)
	defer cleanup()

	// Create and save a block with seen commit
	block := state.MakeBlock(1, types.MakeData(nil), new(types.Commit), nil, state.Validators.GetProposer().Address)
	partSet, err := block.MakePartSet(types.BlockPartSizeBytes)
	require.NoError(t, err)
	seenCommit := makeTestExtCommit(block.Header.Height, cmttime.Now())
	bs.SaveBlockWithExtendedCommit(block, partSet, seenCommit)

	// Load seen commit
	loadedSeenCommit := bs.LoadSeenCommit(1)
	require.NotNil(t, loadedSeenCommit)
	require.Equal(t, seenCommit.ToCommit().Hash(), loadedSeenCommit.Hash())
}

func TestFileBlockStoreSaveSeenCommit(t *testing.T) {
	_, bs, cleanup := makeStateAndFileBlockStore(t)
	defer cleanup()

	// Create a seen commit
	seenCommit := makeTestExtCommit(1, cmttime.Now()).ToCommit()

	// Save seen commit
	err := bs.SaveSeenCommit(1, seenCommit)
	require.NoError(t, err)

	// Load and verify seen commit
	loadedSeenCommit := bs.LoadSeenCommit(1)
	require.NotNil(t, loadedSeenCommit)
	require.Equal(t, seenCommit.Hash(), loadedSeenCommit.Hash())
}

func TestFileBlockStoreIsEmpty(t *testing.T) {
	state, bs, cleanup := makeStateAndFileBlockStore(t)
	defer cleanup()

	// Initially empty
	require.True(t, bs.IsEmpty())

	// Create and save a block
	block := state.MakeBlock(1, types.MakeData(nil), new(types.Commit), nil, state.Validators.GetProposer().Address)
	partSet, err := block.MakePartSet(types.BlockPartSizeBytes)
	require.NoError(t, err)
	seenCommit := makeTestExtCommit(block.Header.Height, cmttime.Now())
	bs.SaveBlockWithExtendedCommit(block, partSet, seenCommit)

	// After saving block
	require.False(t, bs.IsEmpty())
}

func TestFileBlockStoreLoadBaseMeta(t *testing.T) {
	state, bs, cleanup := makeStateAndFileBlockStore(t)
	defer cleanup()

	// Initially empty
	require.Nil(t, bs.LoadBaseMeta())

	// Create and save a block
	block := state.MakeBlock(1, types.MakeData(nil), new(types.Commit), nil, state.Validators.GetProposer().Address)
	partSet, err := block.MakePartSet(types.BlockPartSizeBytes)
	require.NoError(t, err)
	seenCommit := makeTestExtCommit(block.Header.Height, cmttime.Now())
	bs.SaveBlockWithExtendedCommit(block, partSet, seenCommit)

	// Load base meta
	baseMeta := bs.LoadBaseMeta()
	require.NotNil(t, baseMeta)
	require.Equal(t, block.Hash(), baseMeta.BlockID.Hash)
}

func TestFileBlockStoreLoadBlockMetaByHash(t *testing.T) {
	state, bs, cleanup := makeStateAndFileBlockStore(t)
	defer cleanup()

	// Create and save a block
	block := state.MakeBlock(1, types.MakeData(nil), new(types.Commit), nil, state.Validators.GetProposer().Address)
	partSet, err := block.MakePartSet(types.BlockPartSizeBytes)
	require.NoError(t, err)
	seenCommit := makeTestExtCommit(block.Header.Height, cmttime.Now())
	bs.SaveBlockWithExtendedCommit(block, partSet, seenCommit)

	// Load block meta by hash
	meta := bs.LoadBlockMetaByHash(block.Hash())
	require.NotNil(t, meta)
	require.Equal(t, block.Hash(), meta.BlockID.Hash)

	// Test with non-existent hash
	nonExistentHash := []byte("non-existent-hash")
	require.Nil(t, bs.LoadBlockMetaByHash(nonExistentHash))
}

func TestFileBlockStoreBaseHeightSize(t *testing.T) {
	state, bs, cleanup := makeStateAndFileBlockStore(t)
	defer cleanup()

	// Initially empty
	require.Equal(t, int64(0), bs.Base())
	require.Equal(t, int64(0), bs.Height())
	require.Equal(t, int64(0), bs.Size())

	// Create and save a block
	block := state.MakeBlock(1, types.MakeData(nil), new(types.Commit), nil, state.Validators.GetProposer().Address)
	partSet, err := block.MakePartSet(types.BlockPartSizeBytes)
	require.NoError(t, err)
	seenCommit := makeTestExtCommit(block.Header.Height, cmttime.Now())
	bs.SaveBlockWithExtendedCommit(block, partSet, seenCommit)

	// After saving first block
	require.Equal(t, int64(1), bs.Base())
	require.Equal(t, int64(1), bs.Height())
	require.Equal(t, int64(1), bs.Size())

	// Save another block
	block2 := state.MakeBlock(2, types.MakeData(nil), new(types.Commit), nil, state.Validators.GetProposer().Address)
	partSet2, err := block2.MakePartSet(types.BlockPartSizeBytes)
	require.NoError(t, err)
	seenCommit2 := makeTestExtCommit(block2.Header.Height, cmttime.Now())
	bs.SaveBlockWithExtendedCommit(block2, partSet2, seenCommit2)

	// After saving second block
	require.Equal(t, int64(1), bs.Base())
	require.Equal(t, int64(2), bs.Height())
	require.Equal(t, int64(2), bs.Size())
}

func TestFileBlockStoreClose(t *testing.T) {
	_, bs, cleanup := makeStateAndFileBlockStore(t)
	defer cleanup()

	// Close should always succeed for file-based store
	err := bs.Close()
	require.NoError(t, err)
}

func TestFileBlockStoreSaveBlock(t *testing.T) {
	state, bs, cleanup := makeStateAndFileBlockStore(t)
	defer cleanup()

	// Create a block
	block := state.MakeBlock(1, types.MakeData(nil), new(types.Commit), nil, state.Validators.GetProposer().Address)
	partSet, err := block.MakePartSet(types.BlockPartSizeBytes)
	require.NoError(t, err)
	seenCommit := makeTestExtCommit(block.Header.Height, cmttime.Now())

	// Save block using SaveBlock
	bs.SaveBlock(block, partSet, seenCommit.ToCommit())

	// Verify block was saved
	loadedBlock := bs.LoadBlock(1)
	require.NotNil(t, loadedBlock)
	require.Equal(t, block.Hash(), loadedBlock.Hash())

	// Verify block meta was saved
	meta := bs.LoadBlockMeta(1)
	require.NotNil(t, meta)
	require.Equal(t, block.Hash(), meta.BlockID.Hash)

	// Verify commit was saved
	commit := bs.LoadBlockCommit(1)
	require.NotNil(t, commit)
	require.Equal(t, seenCommit.ToCommit().Hash(), commit.Hash())
}

func TestFileBlockStoreLoadBlockExtendedCommitErrors(t *testing.T) {
	state, bs, cleanup := setupFileBlockStore(t)
	defer cleanup()

	// Create and save a block with extended commit
	block, partSet, seenExtCommit := createTestingBlock(t, state, 1, 1)
	bs.SaveBlockWithExtendedCommit(block, partSet, seenExtCommit)

	testCases := []struct {
		name         string
		setup        func(bs *FileBlockStore, height int64)
		checkPanic   bool
		heightToLoad int64
	}{
		{
			name: "corrupted_extended_commit_file",
			setup: func(bs *FileBlockStore, height int64) {
				// Corrupt the extended commit file
				path := bs.getExtendedCommitPath(height)
				corruptFileForTesting(t, path)
			},
			checkPanic:   true,
			heightToLoad: 1,
		},
		{
			name: "invalid_proto_data_in_extended_commit_file",
			setup: func(bs *FileBlockStore, height int64) {
				// Write invalid proto data to extended commit file
				path := bs.getExtendedCommitPath(height)
				err := os.WriteFile(path, []byte("invalid proto data"), 0644)
				require.NoError(t, err)
			},
			checkPanic:   true,
			heightToLoad: 1,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create a new temporary directory for this test case
			tempDir, err := os.MkdirTemp("", fmt.Sprintf("fbs_test_%s_*", tc.name))
			require.NoError(t, err)
			defer os.RemoveAll(tempDir)

			// Create a new block store in the temporary directory
			testBS, err := NewFileBlockStore(tempDir)
			require.NoError(t, err)
			defer testBS.Close()

			// Copy the block and commit files from the original block store
			srcRangeDir := filepath.Join(bs.baseDir, dataDir, blocksDir, getHeightRangeDir(block.Height))
			dstRangeDir := filepath.Join(tempDir, dataDir, blocksDir, getHeightRangeDir(block.Height))
			err = os.MkdirAll(dstRangeDir, 0755)
			require.NoError(t, err)

			// Copy block file
			srcBlockPath := filepath.Join(srcRangeDir, fmt.Sprintf("block_%d.proto", block.Height))
			dstBlockPath := filepath.Join(dstRangeDir, filepath.Base(srcBlockPath))
			blockData, err := os.ReadFile(srcBlockPath)
			require.NoError(t, err)
			err = os.WriteFile(dstBlockPath, blockData, 0644)
			require.NoError(t, err)

			// Copy commit files
			srcCommitPath := filepath.Join(srcRangeDir, fmt.Sprintf("commit_%d.proto", block.Height))
			dstCommitPath := filepath.Join(dstRangeDir, filepath.Base(srcCommitPath))
			commitData, err := os.ReadFile(srcCommitPath)
			require.NoError(t, err)
			err = os.WriteFile(dstCommitPath, commitData, 0644)
			require.NoError(t, err)

			srcExtCommitPath := filepath.Join(srcRangeDir, fmt.Sprintf("extended_commit_%d.proto", block.Height))
			dstExtCommitPath := filepath.Join(dstRangeDir, filepath.Base(srcExtCommitPath))
			extCommitData, err := os.ReadFile(srcExtCommitPath)
			require.NoError(t, err)
			err = os.WriteFile(dstExtCommitPath, extCommitData, 0644)
			require.NoError(t, err)

			// Apply the test case setup
			tc.setup(testBS, tc.heightToLoad)

			if tc.checkPanic {
				require.Panics(t, func() {
					testBS.LoadBlockExtendedCommit(tc.heightToLoad)
				})
			} else {
				require.NotPanics(t, func() {
					extCommit := testBS.LoadBlockExtendedCommit(tc.heightToLoad)
					if extCommit == nil {
						t.Logf("Expected non-nil commit for non-panic case, but got nil")
					}
				})
			}
		})
	}
}

func TestFileBlockStoreLoadBlockStoreState(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "fbs_state_test_*")
	require.NoError(t, err)
	defer func() {
		if t.Failed() {
			t.Logf("TestFileBlockStoreLoadBlockStoreState failed, preserving test directory: %s", tempDir)
			return
		}
		os.RemoveAll(tempDir)
	}()

	// Create state directory
	stateStoreDir := filepath.Join(tempDir, stateDir) // stateDir is a const from file_store.go
	err = os.MkdirAll(stateStoreDir, 0755)
	require.NoError(t, err)

	// Test loading non-existent state file
	loadedBlockStoreState, err := loadBlockStoreState(tempDir)
	require.NoError(t, err)
	require.Equal(t, int64(0), loadedBlockStoreState.Base)
	require.Equal(t, int64(0), loadedBlockStoreState.Height)

	// Create and save state file
	statePath := filepath.Join(stateStoreDir, "state.json")
	testBlockStoreState := cmtstore.BlockStoreState{
		Base:   10,
		Height: 100,
	}
	data, err := proto.Marshal(&testBlockStoreState)
	require.NoError(t, err)
	err = os.WriteFile(statePath, data, 0644)
	require.NoError(t, err)

	// Test loading existing state file
	loadedBlockStoreState, err = loadBlockStoreState(tempDir)
	require.NoError(t, err)
	require.Equal(t, testBlockStoreState.Base, loadedBlockStoreState.Base)
	require.Equal(t, testBlockStoreState.Height, loadedBlockStoreState.Height)

	// Test loading corrupted state file
	corruptFileForTesting(t, statePath)
	_, err = loadBlockStoreState(tempDir)
	require.Error(t, err)
}

func TestFileBlockStoreLoadBlockPartErrors(t *testing.T) {
	state, bs, cleanup := makeStateAndFileBlockStore(t)
	defer cleanup()

	// Test loading non-existent block part
	part := bs.LoadBlockPart(1, 0)
	require.Nil(t, part)

	// Create and save a block
	block := state.MakeBlock(1, types.MakeData(nil), new(types.Commit), nil, state.Validators.GetProposer().Address)
	partSet, err := block.MakePartSet(types.BlockPartSizeBytes)
	require.NoError(t, err)
	seenCommit := makeTestExtCommit(block.Header.Height, cmttime.Now())
	bs.SaveBlockWithExtendedCommit(block, partSet, seenCommit)

	// Test loading invalid part index
	part = bs.LoadBlockPart(1, 999)
	require.Nil(t, part)

	// Test loading part from non-existent height
	part = bs.LoadBlockPart(999, 0)
	require.Nil(t, part)
}

func TestFileBlockStoreLoadBlockCommitErrors(t *testing.T) {
	state, bs, cleanup := makeStateAndFileBlockStore(t)
	defer cleanup()

	// Test loading non-existent commit
	commit := bs.LoadBlockCommit(1)
	require.Nil(t, commit)

	// Create and save a block
	block := state.MakeBlock(1, types.MakeData(nil), new(types.Commit), nil, state.Validators.GetProposer().Address)
	partSet, err := block.MakePartSet(types.BlockPartSizeBytes)
	require.NoError(t, err)
	seenCommit := makeTestExtCommit(block.Header.Height, cmttime.Now())
	bs.SaveBlockWithExtendedCommit(block, partSet, seenCommit)

	// Test loading commit from non-existent height
	commit = bs.LoadBlockCommit(999)
	require.Nil(t, commit)

	// Test cache hit
	commit = bs.LoadBlockCommit(1)
	require.NotNil(t, commit)
	cachedCommit := bs.LoadBlockCommit(1)
	require.Equal(t, commit.Hash(), cachedCommit.Hash())
}

func TestFileBlockStorePruneBlocksErrors(t *testing.T) {
	state, bs, cleanup := makeStateAndFileBlockStore(t)
	defer cleanup()

	// Test pruning with invalid height
	pruned, _, err := bs.PruneBlocks(0, state)
	require.Error(t, err)
	require.Equal(t, uint64(0), pruned)

	// Test pruning beyond latest height
	pruned, _, err = bs.PruneBlocks(1, state)
	require.Error(t, err)
	require.Equal(t, uint64(0), pruned)

	// Create and save multiple blocks
	for h := int64(1); h <= 5; h++ {
		block := state.MakeBlock(h, types.MakeData(nil), new(types.Commit), nil, state.Validators.GetProposer().Address)
		partSet, err := block.MakePartSet(types.BlockPartSizeBytes)
		require.NoError(t, err)
		seenCommit := makeTestExtCommit(block.Header.Height, cmttime.Now())
		bs.SaveBlockWithExtendedCommit(block, partSet, seenCommit)
	}

	// Test pruning below base height
	pruned, _, err = bs.PruneBlocks(0, state)
	require.Error(t, err)
	require.Equal(t, uint64(0), pruned)

	// Test pruning with missing files (should not error)
	pruned, _, err = bs.PruneBlocks(3, state)
	require.NoError(t, err)
	require.Equal(t, uint64(3), pruned)
}

func TestFileBlockStoreDeleteLatestBlockErrors(t *testing.T) {
	state, bs, cleanup := makeStateAndFileBlockStore(t)
	defer cleanup()

	// Test deleting from empty store
	err := bs.DeleteLatestBlock()
	require.Error(t, err)

	// Create and save a block
	block := state.MakeBlock(1, types.MakeData(nil), new(types.Commit), nil, state.Validators.GetProposer().Address)
	partSet, err := block.MakePartSet(types.BlockPartSizeBytes)
	require.NoError(t, err)
	seenCommit := makeTestExtCommit(block.Header.Height, cmttime.Now())
	bs.SaveBlockWithExtendedCommit(block, partSet, seenCommit)

	// Delete the block
	err = bs.DeleteLatestBlock()
	require.NoError(t, err)
	require.Equal(t, int64(0), bs.Height())

	// Test deleting again from empty store
	err = bs.DeleteLatestBlock()
	require.Error(t, err)
}

// Helper function to corrupt a file for testing
func corruptFileForTesting(t *testing.T, path string) {
	f, err := os.OpenFile(path, os.O_WRONLY, 0o644)
	require.NoError(t, err)
	_, err = f.Write([]byte("corrupted"))
	require.NoError(t, err)
	f.Close()
}

// Helper function to setup file block store for testing
func setupFileBlockStore(t *testing.T) (sm.State, *FileBlockStore, func()) {
	return makeStateAndFileBlockStore(t)
}

func TestFileBlockStoreLoadBlockMeta(t *testing.T) {
	state, bs, cleanup := setupFileBlockStore(t)
	defer cleanup()

	// Test loading non-existent block meta
	meta := bs.LoadBlockMeta(1)
	require.Nil(t, meta)

	// Create and save a block
	block := state.MakeBlock(1, types.MakeData(nil), new(types.Commit), nil, state.Validators.GetProposer().Address)
	partSet, err := block.MakePartSet(types.BlockPartSizeBytes)
	require.NoError(t, err)
	seenCommit := makeTestExtCommit(block.Header.Height, cmttime.Now())
	bs.SaveBlockWithExtendedCommit(block, partSet, seenCommit)

	// Test loading block meta - should be derived from block
	meta = bs.LoadBlockMeta(1)
	require.NotNil(t, meta)
	require.Equal(t, block.Hash(), meta.BlockID.Hash)

	// Test cache hit
	cachedMeta := bs.LoadBlockMeta(1)
	require.NotNil(t, cachedMeta)
	require.Equal(t, meta.BlockID.Hash, cachedMeta.BlockID.Hash)

	// Test loading non-existent height
	meta = bs.LoadBlockMeta(999)
	require.Nil(t, meta)
}
