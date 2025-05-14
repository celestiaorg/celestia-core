package store

import (
	"io"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	dbm "github.com/cometbft/cometbft-db"
	"github.com/cometbft/cometbft/internal/test"
	sm "github.com/cometbft/cometbft/state"
)

// setupBlockStore creates a new block store for benchmarking
func setupBlockStore(b *testing.B, storeType string) (sm.State, interface{}, func()) {
	config := test.ResetTestRoot("block_store_bench")
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
		stateStoreDB = dbm.NewMemDB()
		bs = NewBlockStore(dbm.NewMemDB())

		// Add specific cleanup for resources opened in this case
		cleanupFuncs = append(cleanupFuncs, func() {
			if c, ok := stateStoreDB.(io.Closer); ok {
				c.Close() // For MemDB, this is typically a no-op
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

// commonBenchmarkSetup performs the common setup for load benchmarks
func commonBenchmarkSetup(b *testing.B, storeType string, numBlocksToSave int) (sm.State, interface{}, func()) {
	state, bs, cleanup := setupBlockStore(b, storeType)

	// Save some blocks first
	for i := 0; i < numBlocksToSave; i++ {
		block, partSet, seenCommit := createTestingBlock(b, state, int64(i+1), 10)
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

// benchmarkLoadOperation is a helper to run benchmarks for various load operations
func benchmarkLoadOperation(b *testing.B, operationName string, loadFunc func(bs interface{}, height int64)) {
	storeTypes := []string{"file", "db"}
	numPreloadedBlocks := 100

	for _, storeType := range storeTypes {
		b.Run(storeType+"_"+operationName, func(b *testing.B) {
			_, bs, cleanup := commonBenchmarkSetup(b, storeType, numPreloadedBlocks)
			defer cleanup()

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				height := int64(i%numPreloadedBlocks + 1)
				loadFunc(bs, height)
			}
		})
	}
}

func BenchmarkBlockStore_SaveBlock(b *testing.B) {
	storeTypes := []string{"file", "db"}
	for _, storeType := range storeTypes {
		b.Run(storeType, func(b *testing.B) {
			state, bs, cleanup := setupBlockStore(b, storeType)
			defer cleanup()

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				block, partSet, seenCommit := createTestingBlock(b, state, int64(i+1), 10)
				switch s := bs.(type) {
				case *FileBlockStore:
					s.SaveBlockWithExtendedCommit(block, partSet, seenCommit)
				case *BlockStore:
					s.SaveBlockWithExtendedCommit(block, partSet, seenCommit)
				default:
					b.Fatalf("unknown block store type: %T", s)
				}
			}
		})
	}
}

func BenchmarkBlockStore_SaveBlockWithExtendedCommit(b *testing.B) {
	storeTypes := []string{"file", "db"}
	for _, storeType := range storeTypes {
		b.Run(storeType, func(b *testing.B) {
			state, bs, cleanup := setupBlockStore(b, storeType)
			defer cleanup()

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				block, partSet, seenCommit := createTestingBlock(b, state, int64(i+1), 10)
				switch s := bs.(type) {
				case *FileBlockStore:
					s.SaveBlockWithExtendedCommit(block, partSet, seenCommit)
				case *BlockStore:
					s.SaveBlockWithExtendedCommit(block, partSet, seenCommit)
				default:
					b.Fatalf("unknown block store type: %T", s)
				}
			}
		})
	}
}

func BenchmarkBlockStore_LoadBlock(b *testing.B) {
	benchmarkLoadOperation(b, "LoadBlock", func(bsInf interface{}, height int64) {
		switch s := bsInf.(type) {
		case *FileBlockStore:
			_ = s.LoadBlock(height) // Assign to blank identifier to use the result
		case *BlockStore:
			_ = s.LoadBlock(height)
		default:
			b.Fatalf("unknown block store type: %T", s)
		}
	})
}

func BenchmarkBlockStore_LoadBlockMeta(b *testing.B) {
	benchmarkLoadOperation(b, "LoadBlockMeta", func(bsInf interface{}, height int64) {
		switch s := bsInf.(type) {
		case *FileBlockStore:
			// Test both cache hit and cache miss scenarios
			if b.N%2 == 0 {
				s.metaCache.Remove(height) // Force cache miss to test derivation
			}
			_ = s.LoadBlockMeta(height)
		case *BlockStore:
			_ = s.LoadBlockMeta(height)
		default:
			b.Fatalf("unknown block store type: %T", s)
		}
	})
}

func BenchmarkBlockStore_LoadBlockPart(b *testing.B) {
	benchmarkLoadOperation(b, "LoadBlockPart", func(bsInf interface{}, height int64) {
		switch s := bsInf.(type) {
		case *FileBlockStore:
			_ = s.LoadBlockPart(height, 0)
		case *BlockStore:
			_ = s.LoadBlockPart(height, 0)
		default:
			b.Fatalf("unknown block store type: %T", s)
		}
	})
}

func BenchmarkBlockStore_LoadBlockCommit(b *testing.B) {
	benchmarkLoadOperation(b, "LoadBlockCommit", func(bsInf interface{}, height int64) {
		switch s := bsInf.(type) {
		case *FileBlockStore:
			_ = s.LoadBlockCommit(height)
		case *BlockStore:
			_ = s.LoadBlockCommit(height)
		default:
			b.Fatalf("unknown block store type: %T", s)
		}
	})
}

func BenchmarkBlockStore_LoadBlockExtendedCommit(b *testing.B) {
	benchmarkLoadOperation(b, "LoadBlockExtendedCommit", func(bsInf interface{}, height int64) {
		switch s := bsInf.(type) {
		case *FileBlockStore:
			_ = s.LoadBlockExtendedCommit(height)
		case *BlockStore:
			_ = s.LoadBlockExtendedCommit(height)
		default:
			b.Fatalf("unknown block store type: %T", s)
		}
	})
}

func BenchmarkBlockStore_PruneBlocks(b *testing.B) {
	storeTypes := []string{"file", "db"}
	for _, storeType := range storeTypes {
		b.Run(storeType, func(b *testing.B) {
			state, bs, cleanup := commonBenchmarkSetup(b, storeType, 200) // Save more blocks for pruning
			defer cleanup()

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				pruneHeight := int64(50) // Target a prune height
				currentStoreHeight := bs.(interface{ Height() int64 }).Height()
				base := bs.(interface{ Base() int64 }).Base()

				// Ensure pruneHeight is valid and there's something to prune
				if pruneHeight >= base && pruneHeight < currentStoreHeight {
					switch s := bs.(type) {
					case *FileBlockStore:
						_, _, err := s.PruneBlocks(pruneHeight, state)
						if err != nil {
							b.Error(err)
						}
					case *BlockStore:
						_, _, err := s.PruneBlocks(pruneHeight, state) // Corrected: added state argument
						if err != nil {
							b.Error(err)
						}
					default:
						b.Fatalf("unknown block store type: %T", s)
					}
				} else if currentStoreHeight <= 1 { // If store is empty or has 1 block, repopulate a bit
					// Repopulate to ensure pruning can happen in subsequent iterations
					for j := 0; j < 100; j++ {
						block, partSet, seenCommit := createTestingBlock(b, state, currentStoreHeight+int64(j+1), 10)
						switch s := bs.(type) {
						case *FileBlockStore:
							s.SaveBlockWithExtendedCommit(block, partSet, seenCommit)
						case *BlockStore:
							s.SaveBlockWithExtendedCommit(block, partSet, seenCommit)
						}
					}
				}
			}
		})
	}
}

func BenchmarkBlockStore_DeleteLatestBlock(b *testing.B) {
	storeTypes := []string{"file", "db"}
	for _, storeType := range storeTypes {
		b.Run(storeType, func(b *testing.B) {
			state, bs, cleanup := setupBlockStore(b, storeType) // Initial setup
			defer cleanup()

			const numInitialBlocks = 100
			// Pre-populate blocks for deletion
			for i := 0; i < numInitialBlocks; i++ {
				block, partSet, seenCommit := createTestingBlock(b, state, int64(i+1), 10)
				switch s := bs.(type) {
				case *FileBlockStore:
					s.SaveBlockWithExtendedCommit(block, partSet, seenCommit)
				case *BlockStore:
					s.SaveBlockWithExtendedCommit(block, partSet, seenCommit)
				default:
					b.Fatalf("unknown block store type: %T", s)
				}
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				// Ensure there's a block to delete, then re-add one to keep test running
				if bs.(interface{ Height() int64 }).Height() == 0 {
					block, partSet, seenCommit := createTestingBlock(b, state, 1, 10)
					switch s := bs.(type) {
					case *FileBlockStore:
						s.SaveBlockWithExtendedCommit(block, partSet, seenCommit)
					case *BlockStore:
						s.SaveBlockWithExtendedCommit(block, partSet, seenCommit)
					}
				}

				switch s := bs.(type) {
				case *FileBlockStore:
					if err := s.DeleteLatestBlock(); err != nil {
						b.Error(err)
					}
				case *BlockStore:
					if err := s.DeleteLatestBlock(); err != nil {
						b.Error(err)
					}
				default:
					b.Fatalf("unknown block store type: %T", s)
				}
			}
		})
	}
}
