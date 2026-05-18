package config

import (
	"path/filepath"
	"testing"

	"github.com/cockroachdb/pebble"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	dbm "github.com/cometbft/cometbft-db"
)

func TestBuildPebbleOptions_NoCache(t *testing.T) {
	o := buildPebbleOptions(nil)
	require.NotNil(t, o)

	assert.Equal(t, pebbleMemTableSize, o.MemTableSize)
	assert.Equal(t, pebbleMemTableStopWritesThreshold, o.MemTableStopWritesThreshold)
	assert.Equal(t, pebbleL0StopWritesThreshold, o.L0StopWritesThreshold)
	require.NotNil(t, o.MaxConcurrentCompactions)
	assert.Equal(t, pebbleMaxConcurrentCompactions, o.MaxConcurrentCompactions())
	assert.Nil(t, o.Cache, "no cache should be installed when sharedCache is nil")
}

func TestBuildPebbleOptions_SharedCacheInstalledAndSurvivesEnsureDefaults(t *testing.T) {
	cache := pebble.NewCache(64 << 20)
	t.Cleanup(func() { cache.Unref() })

	o := buildPebbleOptions(cache)
	require.NotNil(t, o)

	assert.Same(t, cache, o.Cache, "shared cache should be installed verbatim")
	// EnsureDefaults must not clobber our explicit overrides.
	assert.Equal(t, pebbleMemTableSize, o.MemTableSize)
	assert.Equal(t, pebbleL0StopWritesThreshold, o.L0StopWritesThreshold)
}

func TestBuildGoLevelDBOptions(t *testing.T) {
	o := buildGoLevelDBOptions()
	require.NotNil(t, o)

	assert.Equal(t, goLevelDBWriteBuffer, o.WriteBuffer)
	assert.Equal(t, goLevelDBBlockCacheCapacity, o.BlockCacheCapacity)
	assert.Equal(t, goLevelDBWriteL0SlowdownTrigger, o.WriteL0SlowdownTrigger)
	assert.Equal(t, goLevelDBWriteL0PauseTrigger, o.WriteL0PauseTrigger)
}

func TestNewCompactionDBProvider_PebbleRoundTrip(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.SetRoot(dir)
	cfg.DBBackend = string(dbm.PebbleDBBackend)

	cache := pebble.NewCache(32 << 20)
	t.Cleanup(func() { cache.Unref() })

	provider := NewCompactionDBProvider(cache)
	db, err := provider(&DBContext{
		ID:     "test",
		Config: cfg,
		Path:   filepath.Join(dir, "data"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	require.NoError(t, db.Set([]byte("k"), []byte("v")))
	got, err := db.Get([]byte("k"))
	require.NoError(t, err)
	assert.Equal(t, []byte("v"), got)
}

func TestNewCompactionDBProvider_NilCacheStillWorks(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.SetRoot(dir)
	cfg.DBBackend = string(dbm.PebbleDBBackend)

	provider := NewCompactionDBProvider(nil)
	db, err := provider(&DBContext{
		ID:     "test",
		Config: cfg,
		Path:   filepath.Join(dir, "data"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	require.NoError(t, db.Set([]byte("k"), []byte("v")))
}

func TestNewCompactionDBProvider_UnknownBackendFallsThrough(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.SetRoot(dir)
	cfg.DBBackend = string(dbm.MemDBBackend)

	provider := NewCompactionDBProvider(nil)
	db, err := provider(&DBContext{
		ID:     "test",
		Config: cfg,
		Path:   filepath.Join(dir, "data"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	require.NoError(t, db.Set([]byte("k"), []byte("v")))
}
