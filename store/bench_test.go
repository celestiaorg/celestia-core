package store

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	dbm "github.com/cometbft/cometbft-db"
	"github.com/cometbft/cometbft/internal/test"
	sm "github.com/cometbft/cometbft/state"
	"github.com/cometbft/cometbft/types"
)

// setupBlockStore creates a new block store for benchmarking
// It uses a sanitized b.Name() to ensure unique paths for parallel benchmark runs.
func setupBlockStore(b *testing.B, storeType string) (sm.State, interface{}, func()) {
	// Sanitize b.Name() to replace path separators, making it safe for directory names.
	sanitizedTestName := strings.ReplaceAll(b.Name(), "/", "_")
	config := test.ResetTestRoot(sanitizedTestName)
	var stateStoreDB dbm.DB
	var bs interface{}

	// Base cleanup applicable to all store types
	cleanupFuncs := []func(){
		func() { os.RemoveAll(config.RootDir) },
	}

	switch storeType {
	case "file":
		// Use config.RootDir as the base for temporary directories to keep them out of /tmp
		// and ensure they are cleaned up with config.RootDir.
		tempDir, err := os.MkdirTemp(config.RootDir, "file_bs_bench_")
		require.NoError(b, err)
		stateDBDir, err := os.MkdirTemp(config.RootDir, "state_db_bench_")
		require.NoError(b, err)

		stateStoreDB, err = dbm.NewDB("state", dbm.GoLevelDBBackend, stateDBDir)
		require.NoError(b, err)

		bs, err = NewFileBlockStore(tempDir)
		require.NoError(b, err)

		// Add specific cleanup for resources opened in this case (e.g., stateStoreDB)
		cleanupFuncs = append(cleanupFuncs, func() {
			if c, ok := stateStoreDB.(io.Closer); ok {
				c.Close()
			}
		})

	case "db":
		// Create a directory for the DB implementation
		dbDir, err := os.MkdirTemp(config.RootDir, "db_bs_bench_")
		require.NoError(b, err)
		stateDBDir, err := os.MkdirTemp(config.RootDir, "state_db_bench_")
		require.NoError(b, err)

		stateStoreDB, err = dbm.NewDB("state", dbm.GoLevelDBBackend, stateDBDir)
		require.NoError(b, err)

		db, err := dbm.NewDB("blockstore", dbm.GoLevelDBBackend, dbDir)
		require.NoError(b, err)
		bs = NewBlockStore(db)

		// Add specific cleanup for resources opened in this case
		cleanupFuncs = append(cleanupFuncs, func() {
			if c, ok := stateStoreDB.(io.Closer); ok {
				c.Close()
			}
			if c, ok := db.(io.Closer); ok {
				c.Close()
			}
		})

	default:
		b.Fatalf("unknown store type: %s", storeType)
	}

	// Combined cleanup function that executes all registered cleanup actions in reverse order.
	cleanup := func() {
		for i := len(cleanupFuncs) - 1; i >= 0; i-- {
			cleanupFuncs[i]()
		}
	}

	stateStore := sm.NewStore(stateStoreDB, sm.StoreOptions{
		DiscardABCIResponses: false,
	})
	state, err := stateStore.LoadFromDBOrGenesisFile(config.GenesisFile())
	require.NoError(b, err)

	return state, bs, cleanup
}

func BenchmarkBlockStore_SaveBlock(b *testing.B) {
	storeTypes := []string{"file", "db"}
	const blocksPerBatch = 10 // Number of blocks to save in one benchmark operation

	for _, storeType := range storeTypes {
		b.Run(storeType, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				// Create a fresh store for each iteration
				state, bs, cleanup := setupBlockStore(b, storeType)
				// Prepare a batch of blocks outside the timer
				blocks := make([]*types.Block, blocksPerBatch)
				partSets := make([]*types.PartSet, blocksPerBatch)
				commits := make([]*types.ExtendedCommit, blocksPerBatch)
				for j := 0; j < blocksPerBatch; j++ {
					blocks[j], partSets[j], commits[j] = createTestingBlock(b, state, int64(j+1), 100)
				}

				b.ResetTimer()
				b.StartTimer()

				// Save all blocks within the timing measurement
				for j := 0; j < blocksPerBatch; j++ {
					switch s := bs.(type) {
					case *FileBlockStore:
						s.SaveBlockWithExtendedCommit(blocks[j], partSets[j], commits[j])
					case *BlockStore:
						s.SaveBlockWithExtendedCommit(blocks[j], partSets[j], commits[j])
					default:
						b.Fatalf("unknown block store type: %T", s)
					}
				}

				// Ensure data is flushed to disk before stopping the timer
				switch s := bs.(type) {
				case *FileBlockStore:
					if err := s.Close(); err != nil {
						b.Error(err)
					}
				case *BlockStore:
					if err := s.Close(); err != nil {
						b.Error(err)
					}
				}

				b.StopTimer() // Stop timer before per-iteration cleanup
				cleanup()
			}
		})
	}
}

func BenchmarkBlockStore_SaveBlockWithExtendedCommit(b *testing.B) {
	storeTypes := []string{"file", "db"}
	const blocksPerBatch = 100 // Number of blocks to save in one benchmark operation

	for _, storeType := range storeTypes {
		b.Run(storeType, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				// Create a fresh store for each iteration
				state, bs, cleanup := setupBlockStore(b, storeType)

				// Prepare a batch of blocks
				blocks := make([]*types.Block, blocksPerBatch)
				partSets := make([]*types.PartSet, blocksPerBatch)
				commits := make([]*types.ExtendedCommit, blocksPerBatch)
				for j := 0; j < blocksPerBatch; j++ {
					blocks[j], partSets[j], commits[j] = createTestingBlock(b, state, int64(j+1), 100)
				}

				b.ResetTimer()
				b.StartTimer()

				// Save all blocks within the timing measurement
				for j := 0; j < blocksPerBatch; j++ {
					switch s := bs.(type) {
					case *FileBlockStore:
						s.SaveBlockWithExtendedCommit(blocks[j], partSets[j], commits[j])
					case *BlockStore:
						s.SaveBlockWithExtendedCommit(blocks[j], partSets[j], commits[j])
					default:
						b.Fatalf("unknown block store type: %T", s)
					}
				}

				// Ensure data is flushed to disk before stopping the timer
				switch s := bs.(type) {
				case *FileBlockStore:
					if err := s.Close(); err != nil {
						b.Error(err)
					}
				case *BlockStore:
					if err := s.Close(); err != nil {
						b.Error(err)
					}
				}
				b.StopTimer()
				cleanup()
			}
		})
	}
}

// Helper function to setup and preload a blockstore for read benchmarks
func setupPreloadedBlockStore(b *testing.B, storeType string, numBlocks int) (sm.State, interface{}, func()) {
	state, bs, cleanup := setupBlockStore(b, storeType)

	// Preload blocks
	for i := 0; i < numBlocks; i++ {
		block, partSet, seenCommit := createTestingBlock(b, state, int64(i+1), 100)
		switch s := bs.(type) {
		case *FileBlockStore:
			s.SaveBlockWithExtendedCommit(block, partSet, seenCommit)
		case *BlockStore:
			s.SaveBlockWithExtendedCommit(block, partSet, seenCommit)
		default:
			b.Fatalf("unknown block store type: %T", s)
		}
	}

	return state, bs, cleanup
}

func BenchmarkBlockStore_LoadBlock(b *testing.B) {
	storeTypes := []string{"file", "db"}
	const blocksPerBatch = 100 // Number of blocks to load in one benchmark operation
	const numPreloadedBlocks = 100

	for _, storeType := range storeTypes {
		b.Run(storeType, func(b *testing.B) {
			b.StopTimer()
			_, bs, cleanup := setupPreloadedBlockStore(b, storeType, numPreloadedBlocks)
			defer cleanup()
			b.StartTimer() // Start timer before ResetTimer (will be zeroed)

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				b.StartTimer()
				// Load a batch of blocks within the timing measurement
				for j := 0; j < blocksPerBatch; j++ {
					height := int64((j % numPreloadedBlocks) + 1)
					switch s := bs.(type) {
					case *FileBlockStore:
						_ = s.LoadBlock(height)
					case *BlockStore:
						_ = s.LoadBlock(height)
					default:
						b.Fatalf("unknown block store type: %T", s)
					}
				}
				b.StopTimer()
			}
		})
	}
}

func BenchmarkBlockStore_LoadBlockMeta(b *testing.B) {
	storeTypes := []string{"file", "db"}
	const blocksPerBatch = 100 // Number of block metas to load in one benchmark operation
	const numPreloadedBlocks = 100

	for _, storeType := range storeTypes {
		b.Run(storeType, func(b *testing.B) {
			b.StopTimer()
			_, bs, cleanup := setupPreloadedBlockStore(b, storeType, numPreloadedBlocks)
			defer cleanup()
			b.StartTimer() // Start timer before ResetTimer (will be zeroed)

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				b.StartTimer()
				// Load a batch of block metas within the timing measurement
				for j := 0; j < blocksPerBatch; j++ {
					height := int64((j % numPreloadedBlocks) + 1)
					switch s := bs.(type) {
					case *FileBlockStore:
						// Test both cache hit and cache miss scenarios
						if j%2 == 0 {
							s.metaCache.Remove(height) // Force cache miss to test derivation
						}
						_ = s.LoadBlockMeta(height)
					case *BlockStore:
						_ = s.LoadBlockMeta(height)
					default:
						b.Fatalf("unknown block store type: %T", s)
					}
				}
				b.StopTimer()
			}
		})
	}
}

func BenchmarkBlockStore_LoadBlockPart(b *testing.B) {
	storeTypes := []string{"file", "db"}
	const blocksPerBatch = 100 // Number of block parts to load in one benchmark operation
	const numPreloadedBlocks = 100

	for _, storeType := range storeTypes {
		b.Run(storeType, func(b *testing.B) {
			b.StopTimer()
			_, bs, cleanup := setupPreloadedBlockStore(b, storeType, numPreloadedBlocks)
			defer cleanup()
			b.StartTimer()

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				b.StartTimer()
				// Load a batch of block parts within the timing measurement
				for j := 0; j < blocksPerBatch; j++ {
					height := int64((j % numPreloadedBlocks) + 1)
					switch s := bs.(type) {
					case *FileBlockStore:
						_ = s.LoadBlockPart(height, 0)
					case *BlockStore:
						_ = s.LoadBlockPart(height, 0)
					default:
						b.Fatalf("unknown block store type: %T", s)
					}
				}
				b.StopTimer()
			}
		})
	}
}

func BenchmarkBlockStore_LoadBlockCommit(b *testing.B) {
	storeTypes := []string{"file", "db"}
	const blocksPerBatch = 100 // Number of block commits to load in one benchmark operation
	const numPreloadedBlocks = 100

	for _, storeType := range storeTypes {
		b.Run(storeType, func(b *testing.B) {
			b.StopTimer()
			_, bs, cleanup := setupPreloadedBlockStore(b, storeType, numPreloadedBlocks)
			defer cleanup()
			b.StartTimer()

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				b.StartTimer()
				// Load a batch of block commits within the timing measurement
				for j := 0; j < blocksPerBatch; j++ {
					height := int64((j % numPreloadedBlocks) + 1)
					switch s := bs.(type) {
					case *FileBlockStore:
						_ = s.LoadBlockCommit(height)
					case *BlockStore:
						_ = s.LoadBlockCommit(height)
					default:
						b.Fatalf("unknown block store type: %T", s)
					}
				}
				b.StopTimer()
			}
		})
	}
}

func BenchmarkBlockStore_LoadBlockExtendedCommit(b *testing.B) {
	storeTypes := []string{"file", "db"}
	const blocksPerBatch = 100 // Number of extended commits to load in one benchmark operation
	const numPreloadedBlocks = 100

	for _, storeType := range storeTypes {
		b.Run(storeType, func(b *testing.B) {
			b.StopTimer()
			_, bs, cleanup := setupPreloadedBlockStore(b, storeType, numPreloadedBlocks)
			defer cleanup()
			b.StartTimer() // Start timer before ResetTimer (will be zeroed)

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				b.StartTimer()
				// Load a batch of extended commits within the timing measurement
				for j := 0; j < blocksPerBatch; j++ {
					height := int64((j % numPreloadedBlocks) + 1)
					switch s := bs.(type) {
					case *FileBlockStore:
						_ = s.LoadBlockExtendedCommit(height)
					case *BlockStore:
						_ = s.LoadBlockExtendedCommit(height)
					default:
						b.Fatalf("unknown block store type: %T", s)
					}
				}
				b.StopTimer()
			}
		})
	}
}

func BenchmarkBlockStore_PruneBlocks(b *testing.B) {
	storeTypes := []string{"file", "db"}
	const initialBlockCount = 200

	for _, storeType := range storeTypes {
		b.Run(storeType, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				b.StopTimer() // Stop timer for per-iteration setup

				// Create a fresh store for each iteration
				state, bs, cleanup := setupBlockStore(b, storeType)

				// Prepare initial blocks
				for j := 0; j < initialBlockCount; j++ {
					block, partSet, seenCommit := createTestingBlock(b, state, int64(j+1), 100)
					switch s := bs.(type) {
					case *FileBlockStore:
						s.SaveBlockWithExtendedCommit(block, partSet, seenCommit)
					case *BlockStore:
						s.SaveBlockWithExtendedCommit(block, partSet, seenCommit)
					default:
						b.Fatalf("unknown block store type: %T", s)
					}
				}
				// DO NOT perform an intermediate close here. Rely on the Close() within the timed section.

				b.ResetTimer() // Reset timer after setup, before measured work
				b.StartTimer() // Start timing the actual operations

				// Prune blocks to the middle of the range
				pruneHeight := int64(initialBlockCount / 2)
				switch s := bs.(type) {
				case *FileBlockStore:
					_, _, err := s.PruneBlocks(pruneHeight, state)
					if err != nil {
						b.Error(err)
					}
					// Ensure data is flushed to disk
					if err := s.Close(); err != nil {
						b.Error(err)
					}
				case *BlockStore:
					_, _, err := s.PruneBlocks(pruneHeight, state)
					if err != nil {
						b.Error(err)
					}
					// Ensure data is flushed to disk
					if dbc, ok := s.db.(io.Closer); ok {
						if err := dbc.Close(); err != nil {
							b.Error(err)
						}
					} else {
						b.Logf("Warning: BlockStore.db is not an io.Closer for type %T", s.db)
					}
				}

				b.StopTimer() // Stop timer before per-iteration cleanup
				cleanup()
			}
		})
	}
}

func BenchmarkBlockStore_DeleteLatestBlock(b *testing.B) {
	storeTypes := []string{"file", "db"}
	const blocksPerBatch = 10     // Number of blocks to delete in one benchmark operation
	const initialBlockCount = 100 // Fewer blocks needed than for pruning.

	for _, storeType := range storeTypes {
		b.Run(storeType, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				b.StopTimer() // Stop timer for per-iteration setup

				// Create a fresh store for each iteration
				state, bs, cleanup := setupBlockStore(b, storeType)

				// Prepare initial blocks
				for j := 0; j < initialBlockCount; j++ {
					block, partSet, seenCommit := createTestingBlock(b, state, int64(j+1), 100)
					switch s := bs.(type) {
					case *FileBlockStore:
						s.SaveBlockWithExtendedCommit(block, partSet, seenCommit)
					case *BlockStore:
						s.SaveBlockWithExtendedCommit(block, partSet, seenCommit)
					default:
						b.Fatalf("unknown block store type: %T", s)
					}
				}
				// DO NOT perform an intermediate close here. Rely on the Close() within the timed section.

				b.ResetTimer() // Reset timer after setup, before measured work
				b.StartTimer() // Start timing the actual operations

				// Delete multiple blocks per operation
				for j := 0; j < blocksPerBatch; j++ {
					if bs.(interface{ Height() int64 }).Height() == 0 {
						break // Stop deleting if no blocks left for this iteration.
					}

					switch s := bs.(type) {
					case *FileBlockStore:
						if err := s.DeleteLatestBlock(); err != nil {
							if !(s.Height() == 0 && err.Error() == "no last block to delete") {
								b.Error(err)
							}
							break
						}
					case *BlockStore:
						if err := s.DeleteLatestBlock(); err != nil {
							if !(s.Height() == 0 && err.Error() == "no last block to delete") {
								b.Error(err)
							}
							break
						}
					}
				}

				// Ensure data is flushed to disk
				switch s := bs.(type) {
				case *FileBlockStore:
					if err := s.Close(); err != nil {
						b.Error(err)
					}
				case *BlockStore:
					if dbc, ok := s.db.(io.Closer); ok {
						if err := dbc.Close(); err != nil {
							b.Error(err)
						}
					} else {
						b.Logf("Warning: BlockStore.db is not an io.Closer for type %T", s.db)
					}
				}
				b.StopTimer() // Stop timer before per-iteration cleanup
				cleanup()
			}
		})
	}
}
