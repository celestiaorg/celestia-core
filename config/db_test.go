package config

import (
	"bytes"
	"strconv"
	"testing"
	"time"

	dbm "github.com/cometbft/cometbft-db"
	"github.com/stretchr/testify/require"
)

// writeLargeBatch opens id through DefaultDBProvider and writes one synced 8 MiB batch.
func writeLargeBatch(t *testing.T, cfg *Config, id string) dbm.DB {
	t.Helper()
	db, err := DefaultDBProvider(&DBContext{ID: id, Config: cfg})
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	b := db.NewBatch()
	defer b.Close()
	value := make([]byte, 64<<10)
	for i := 0; i < 128; i++ {
		require.NoError(t, b.Set([]byte(strconv.Itoa(i)), value))
	}
	require.NoError(t, b.WriteSync())
	return db
}

func TestDefaultDBProviderBlockstoreGoLevelDB(t *testing.T) {
	cfg := DefaultConfig()
	cfg.SetRoot(t.TempDir())
	cfg.DBBackend = string(dbm.GoLevelDBBackend)
	// Level-0 tables right after the write mean the batch took the large-batch transaction path.
	l0 := func(id string) int {
		n, err := writeLargeBatch(t, cfg, id).(*dbm.GoLevelDB).DB().GetProperty("leveldb.num-files-at-level0")
		require.NoError(t, err)
		l0, err := strconv.Atoi(n)
		require.NoError(t, err)
		return l0
	}
	require.Zero(t, l0("blockstore"))
	require.Positive(t, l0("state"), "other DBs keep engine defaults")

	cfg.SetRoot(t.TempDir())
	cfg.Storage = &StorageConfig{}
	require.Positive(t, l0("blockstore"), "zero values keep engine defaults")
}

func TestDefaultDBProviderBlockstorePebble(t *testing.T) {
	cfg := DefaultConfig()
	cfg.SetRoot(t.TempDir())
	cfg.DBBackend = string(dbm.PebbleDBBackend)
	// A batch over half the memtable becomes a flushable batch, which pebble flushes at once.
	flushed := func(db dbm.DB) func() bool {
		return func() bool { return db.(*dbm.PebbleDB).DB().Metrics().Flush.Count > 0 }
	}
	require.Never(t, flushed(writeLargeBatch(t, cfg, "blockstore")), 500*time.Millisecond, 10*time.Millisecond)
	require.Eventually(t, flushed(writeLargeBatch(t, cfg, "state")), 5*time.Second, 10*time.Millisecond,
		"other DBs keep engine defaults")
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
			value := bytes.Repeat([]byte{1, 2, 3, 4}, 2<<20)
			require.NoError(t, db.SetSync([]byte("k"), value))
			require.NoError(t, db.Close())

			// The write is replayed from the journal/WAL, with the tuned options and with engine defaults.
			for _, storage := range []*StorageConfig{DefaultStorageConfig(), {}} {
				cfg.Storage = storage
				db, err = DefaultDBProvider(ctx)
				require.NoError(t, err)
				got, err := db.Get([]byte("k"))
				require.NoError(t, err)
				require.True(t, bytes.Equal(value, got))
				require.NoError(t, db.Close())
			}
		})
	}
}
