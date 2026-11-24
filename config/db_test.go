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

func TestAutoPebbleSizing(t *testing.T) {
	const gib = 1 << 10
	for _, tc := range []struct{ ram, reserve, want int64 }{
		{0, 20 * gib, 1 * gib},     // unknown memory
		{8 * gib, 20 * gib, 256},   // floor
		{16 * gib, 20 * gib, 256},  // floor
		{32 * gib, 20 * gib, 4915}, // the app keeps its 20 GiB
		{64 * gib, 20 * gib, 18022},
		{128 * gib, 20 * gib, 44236},
		{512 * gib, 20 * gib, 64 * gib}, // cap
		{32 * gib, 0, 13107},
	} {
		require.Equal(t, tc.want, AutoPebbleCacheMB(tc.ram, tc.reserve), "ram %d reserve %d", tc.ram, tc.reserve)
	}
	for cpus, want := range map[int]int{1: 2, 4: 2, 8: 2, 16: 4, 32: 8, 128: 8} {
		require.Equal(t, want, AutoPebbleCompactions(cpus), "cpus %d", cpus)
	}
	// 32 GiB stays well clear of OOM: twice the cache (Go heap) plus the app's
	// reserve fits.
	require.Less(t, 2*AutoPebbleCacheMB(32*gib, 20*gib)+20*gib, int64(32*gib))
}

func TestDefaultDBProviderPebbleTuning(t *testing.T) {
	cfg := DefaultConfig()
	cfg.SetRoot(t.TempDir())
	cfg.DBBackend = string(dbm.PebbleDBBackend)
	cfg.Storage.PebbleCacheSizeMB = 16
	require.True(t, cfg.Storage.PebbleTuning)

	write := func(id string) *dbm.PebbleDB {
		db, err := DefaultDBProvider(&DBContext{ID: id, Config: cfg})
		require.NoError(t, err)
		for i := 0; i < 1000; i++ {
			require.NoError(t, db.Set([]byte(strconv.Itoa(i)), bytes.Repeat([]byte{byte(i)}, 100)))
		}
		p := db.(*dbm.PebbleDB)
		require.NoError(t, p.DB().Flush())
		for i := 0; i < 1000; i++ {
			got, err := db.Get([]byte(strconv.Itoa(i)))
			require.NoError(t, err)
			require.Len(t, got, 100)
		}
		return p
	}
	a, b := write("state"), write("evidence")
	// Both DBs report the same cache, so it is shared.
	require.Positive(t, a.DB().Metrics().BlockCache.Count)
	require.Equal(t, a.DB().Metrics().BlockCache, b.DB().Metrics().BlockCache)
	require.NoError(t, a.Close())
	require.NoError(t, b.Close())

	// Tables written with the tuned options open with engine defaults.
	cfg.Storage.PebbleTuning = false
	db, err := DefaultDBProvider(&DBContext{ID: "state", Config: cfg})
	require.NoError(t, err)
	got, err := db.Get([]byte("7"))
	require.NoError(t, err)
	require.Equal(t, bytes.Repeat([]byte{7}, 100), got)
	require.NoError(t, db.Close())
}
