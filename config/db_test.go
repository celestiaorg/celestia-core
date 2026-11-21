package config

import (
	"strconv"
	"testing"

	dbm "github.com/cometbft/cometbft-db"
	"github.com/stretchr/testify/require"
)

func TestBlockstoreDBOptions(t *testing.T) {
	cfg := DefaultStorageConfig()
	ldb := blockstoreGoLevelDBOptions(cfg)
	require.Equal(t, 64<<20, ldb.GetWriteBuffer())
	require.True(t, ldb.GetDisableLargeBatchTransaction())
	require.Equal(t, uint64(64<<20), blockstorePebbleOptions(cfg).MemTableSize)

	// 0 keeps the engine defaults.
	cfg = &StorageConfig{}
	require.Equal(t, 4<<20, blockstoreGoLevelDBOptions(cfg).GetWriteBuffer())
	require.False(t, blockstoreGoLevelDBOptions(cfg).GetDisableLargeBatchTransaction())
	peb := blockstorePebbleOptions(cfg)
	peb.EnsureDefaults()
	require.Equal(t, uint64(4<<20), peb.MemTableSize)
}

// l0AfterLargeBatch writes one synced 8 MiB batch and returns the goleveldb level-0
// table count: > 0 means it took the large-batch transaction path.
func l0AfterLargeBatch(t *testing.T, cfg *Config, id string) int {
	t.Helper()
	db, err := DefaultDBProvider(&DBContext{ID: id, Config: cfg})
	require.NoError(t, err)
	defer db.Close()
	b := db.NewBatch()
	defer b.Close()
	value := make([]byte, 64<<10)
	for i := 0; i < 128; i++ {
		require.NoError(t, b.Set([]byte(strconv.Itoa(i)), value))
	}
	require.NoError(t, b.WriteSync())
	n, err := db.(*dbm.GoLevelDB).DB().GetProperty("leveldb.num-files-at-level0")
	require.NoError(t, err)
	l0, err := strconv.Atoi(n)
	require.NoError(t, err)
	return l0
}

func TestDefaultDBProviderBlockstoreGoLevelDB(t *testing.T) {
	cfg := DefaultConfig()
	cfg.SetRoot(t.TempDir())
	cfg.DBBackend = string(dbm.GoLevelDBBackend)

	require.Zero(t, l0AfterLargeBatch(t, cfg, "blockstore"), "blockstore batch should go through the memtable")
	require.Positive(t, l0AfterLargeBatch(t, cfg, "state"), "other DBs keep engine defaults")

	cfg.SetRoot(t.TempDir())
	cfg.Storage = &StorageConfig{}
	require.Positive(t, l0AfterLargeBatch(t, cfg, "blockstore"), "zero values keep engine defaults")
}

func TestDefaultDBProviderBlockstoreReopen(t *testing.T) {
	for _, backend := range []dbm.BackendType{dbm.GoLevelDBBackend, dbm.PebbleDBBackend} {
		t.Run(string(backend), func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.SetRoot(t.TempDir())
			cfg.DBBackend = string(backend)
			ctx := &DBContext{ID: "blockstore", Config: cfg}

			db, err := DefaultDBProvider(ctx)
			require.NoError(t, err)
			value := make([]byte, 32<<20)
			value[len(value)-1] = 1
			require.NoError(t, db.SetSync([]byte("k"), value))
			require.NoError(t, db.Close())

			// Reopen with engine defaults and with the tuned options: the on-disk format is unchanged.
			for _, storage := range []*StorageConfig{{}, DefaultStorageConfig()} {
				cfg.Storage = storage
				db, err = DefaultDBProvider(ctx)
				require.NoError(t, err)
				got, err := db.Get([]byte("k"))
				require.NoError(t, err)
				require.Equal(t, value, got)
				require.NoError(t, db.Close())
			}
		})
	}
}
