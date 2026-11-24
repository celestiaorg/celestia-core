package config

import (
	"bytes"
	"strconv"
	"testing"

	dbm "github.com/cometbft/cometbft-db"
	"github.com/stretchr/testify/require"
)

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
