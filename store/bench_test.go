package store

import (
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
	stateStore := sm.NewStore(dbm.NewMemDB(), sm.StoreOptions{
		DiscardABCIResponses: false,
	})
	state, err := stateStore.LoadFromDBOrGenesisFile(config.GenesisFile())
	require.NoError(b, err)

	var bs interface{}
	var cleanup func()

	switch storeType {
	case "file":
		// Create a temporary directory for the file block store
		tempDir, err := os.MkdirTemp("", "file_block_store_bench")
		require.NoError(b, err)
		bs, err = NewFileBlockStore(tempDir)
		require.NoError(b, err)
		cleanup = func() {
			os.RemoveAll(config.RootDir)
			os.RemoveAll(tempDir)
		}
	case "db":
		db := dbm.NewMemDB()
		bs = NewBlockStore(db)
		cleanup = func() {
			os.RemoveAll(config.RootDir)
		}
	default:
		b.Fatalf("unknown store type: %s", storeType)
	}

	return state, bs, cleanup
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
				}
			}
		})
	}
}

func BenchmarkBlockStore_LoadBlock(b *testing.B) {
	storeTypes := []string{"file", "db"}
	for _, storeType := range storeTypes {
		b.Run(storeType, func(b *testing.B) {
			state, bs, cleanup := setupBlockStore(b, storeType)
			defer cleanup()

			// Save some blocks first
			for i := 0; i < 100; i++ {
				block, partSet, seenCommit := createTestingBlock(b, state, int64(i+1), 10)
				switch s := bs.(type) {
				case *FileBlockStore:
					s.SaveBlockWithExtendedCommit(block, partSet, seenCommit)
				case *BlockStore:
					s.SaveBlockWithExtendedCommit(block, partSet, seenCommit)
				}
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				height := int64(i%100 + 1)
				switch s := bs.(type) {
				case *FileBlockStore:
					s.LoadBlock(height)
				case *BlockStore:
					s.LoadBlock(height)
				}
			}
		})
	}
}

func BenchmarkBlockStore_LoadBlockMeta(b *testing.B) {
	storeTypes := []string{"file", "db"}
	for _, storeType := range storeTypes {
		b.Run(storeType, func(b *testing.B) {
			state, bs, cleanup := setupBlockStore(b, storeType)
			defer cleanup()

			// Save some blocks first
			for i := 0; i < 100; i++ {
				block, partSet, seenCommit := createTestingBlock(b, state, int64(i+1), 10)
				switch s := bs.(type) {
				case *FileBlockStore:
					s.SaveBlockWithExtendedCommit(block, partSet, seenCommit)
				case *BlockStore:
					s.SaveBlockWithExtendedCommit(block, partSet, seenCommit)
				}
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				height := int64(i%100 + 1)
				switch s := bs.(type) {
				case *FileBlockStore:
					s.LoadBlockMeta(height)
				case *BlockStore:
					s.LoadBlockMeta(height)
				}
			}
		})
	}
}

func BenchmarkBlockStore_LoadBlockPart(b *testing.B) {
	storeTypes := []string{"file", "db"}
	for _, storeType := range storeTypes {
		b.Run(storeType, func(b *testing.B) {
			state, bs, cleanup := setupBlockStore(b, storeType)
			defer cleanup()

			// Save some blocks first
			for i := 0; i < 100; i++ {
				block, partSet, seenCommit := createTestingBlock(b, state, int64(i+1), 10)
				switch s := bs.(type) {
				case *FileBlockStore:
					s.SaveBlockWithExtendedCommit(block, partSet, seenCommit)
				case *BlockStore:
					s.SaveBlockWithExtendedCommit(block, partSet, seenCommit)
				}
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				height := int64(i%100 + 1)
				switch s := bs.(type) {
				case *FileBlockStore:
					s.LoadBlockPart(height, 0)
				case *BlockStore:
					s.LoadBlockPart(height, 0)
				}
			}
		})
	}
}

func BenchmarkBlockStore_LoadBlockCommit(b *testing.B) {
	storeTypes := []string{"file", "db"}
	for _, storeType := range storeTypes {
		b.Run(storeType, func(b *testing.B) {
			state, bs, cleanup := setupBlockStore(b, storeType)
			defer cleanup()

			// Save some blocks first
			for i := 0; i < 100; i++ {
				block, partSet, seenCommit := createTestingBlock(b, state, int64(i+1), 10)
				switch s := bs.(type) {
				case *FileBlockStore:
					s.SaveBlockWithExtendedCommit(block, partSet, seenCommit)
				case *BlockStore:
					s.SaveBlockWithExtendedCommit(block, partSet, seenCommit)
				}
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				height := int64(i%100 + 1)
				switch s := bs.(type) {
				case *FileBlockStore:
					s.LoadBlockCommit(height)
				case *BlockStore:
					s.LoadBlockCommit(height)
				}
			}
		})
	}
}

func BenchmarkBlockStore_LoadBlockExtendedCommit(b *testing.B) {
	storeTypes := []string{"file", "db"}
	for _, storeType := range storeTypes {
		b.Run(storeType, func(b *testing.B) {
			state, bs, cleanup := setupBlockStore(b, storeType)
			defer cleanup()

			// Save some blocks first
			for i := 0; i < 100; i++ {
				block, partSet, seenCommit := createTestingBlock(b, state, int64(i+1), 10)
				switch s := bs.(type) {
				case *FileBlockStore:
					s.SaveBlockWithExtendedCommit(block, partSet, seenCommit)
				case *BlockStore:
					s.SaveBlockWithExtendedCommit(block, partSet, seenCommit)
				}
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				height := int64(i%100 + 1)
				switch s := bs.(type) {
				case *FileBlockStore:
					s.LoadBlockExtendedCommit(height)
				case *BlockStore:
					s.LoadBlockExtendedCommit(height)
				}
			}
		})
	}
}

func BenchmarkBlockStore_PruneBlocks(b *testing.B) {
	storeTypes := []string{"file", "db"}
	for _, storeType := range storeTypes {
		b.Run(storeType, func(b *testing.B) {
			state, bs, cleanup := setupBlockStore(b, storeType)
			defer cleanup()

			// Save some blocks first
			for i := 0; i < 100; i++ {
				block, partSet, seenCommit := createTestingBlock(b, state, int64(i+1), 10)
				switch s := bs.(type) {
				case *FileBlockStore:
					s.SaveBlockWithExtendedCommit(block, partSet, seenCommit)
				case *BlockStore:
					s.SaveBlockWithExtendedCommit(block, partSet, seenCommit)
				}
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				height := int64(i%100 + 1)
				switch s := bs.(type) {
				case *FileBlockStore:
					s.PruneBlocks(height, state)
				case *BlockStore:
					s.PruneBlocks(height, state)
				}
			}
		})
	}
}

func BenchmarkBlockStore_DeleteLatestBlock(b *testing.B) {
	storeTypes := []string{"file", "db"}
	for _, storeType := range storeTypes {
		b.Run(storeType, func(b *testing.B) {
			state, bs, cleanup := setupBlockStore(b, storeType)
			defer cleanup()

			// Save some blocks first
			for i := 0; i < 100; i++ {
				block, partSet, seenCommit := createTestingBlock(b, state, int64(i+1), 10)
				switch s := bs.(type) {
				case *FileBlockStore:
					s.SaveBlockWithExtendedCommit(block, partSet, seenCommit)
				case *BlockStore:
					s.SaveBlockWithExtendedCommit(block, partSet, seenCommit)
				}
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				switch s := bs.(type) {
				case *FileBlockStore:
					s.DeleteLatestBlock()
				case *BlockStore:
					s.DeleteLatestBlock()
				}
			}
		})
	}
}
