package config

import (
	"context"
	"runtime"

	"github.com/cockroachdb/pebble"

	dbm "github.com/cometbft/cometbft-db"

	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/libs/service"
)

// ServiceProvider takes a config and a logger and returns a ready to go Node.
type ServiceProvider func(context.Context, *Config, log.Logger) (service.Service, error)

// DBContext specifies config information for loading a new DB.
type DBContext struct {
	ID     string
	Config *Config
	Path   string
}

// DBProvider takes a DBContext and returns an instantiated DB.
type DBProvider func(*DBContext) (dbm.DB, error)

// DefaultDBProvider returns a database using the DBBackend and DBDir
// specified in the Config, with library-default options. Used by nodes when
// DB tuning is disabled, and by short-lived tools (inspect, light client)
// regardless of the tuning setting.
func DefaultDBProvider(ctx *DBContext) (dbm.DB, error) {
	dbType := dbm.BackendType(ctx.Config.DBBackend)
	path := ctx.Path
	if path == "" {
		path = ctx.Config.DBDir()
	}
	return dbm.NewDB(ctx.ID, dbType, path)
}

// Hardcoded DB-tuning values applied only when Storage.DBTuning is enabled.
// See celestiaorg/celestia-core#3053: with tuning off, the library defaults
// are fine; with tuning on, larger memtables, growing sstable target sizes and
// a shared cache keep the LSM from fragmenting into millions of tiny sstables
// on large stores and keep block-save latency bounded under load.
const (
	// PebbleSharedCacheBytes is the size of the shared pebble block cache
	// installed across blockstore, state, evidence, and tx_index when
	// DB tuning is enabled and the backend is pebbledb.
	PebbleSharedCacheBytes int64 = 512 << 20 // 512 MiB

	pebbleMemTableSize                uint64 = 64 << 20 // 64 MiB
	pebbleMemTableStopWritesThreshold int    = 4
	pebbleL0StopWritesThreshold       int    = 24

	// pebbleL0TargetFileSize is the sstable target size for L0. Pebble's
	// default is 2 MiB, which on a multi-TB archival blockstore that compaction
	// can't keep up with degenerates into millions of tiny sstables. A larger
	// base — doubled per level and capped at pebbleMaxTargetFileSize — makes
	// each compaction emit fewer, larger files and keeps the LSM's file count
	// (and the memory Pebble spends tracking it) bounded, without the large
	// per-compaction IO spikes that a very large target would cause. See
	// celestiaorg/celestia-core#3053.
	pebbleL0TargetFileSize  int64 = 8 << 20   // 8 MiB
	pebbleMaxTargetFileSize int64 = 128 << 20 // 128 MiB (matches pebble's default L6)
	pebbleNumLevels               = 7
)

// pebbleMaxConcurrentCompactions depends on GOMAXPROCS, so it is computed at
// init rather than declared as a const.
var pebbleMaxConcurrentCompactions = max(2, runtime.GOMAXPROCS(0)/4)

// NewCompactionDBProvider returns a DBProvider that opens PebbleDB with the
// compaction-friendly tuning above, optionally sharing sharedPebbleCache
// (nil = per-DB cache). Non-pebble backends fall back to library defaults.
// Use it only when Storage.DBTuning is enabled and the backend is pebbledb.
func NewCompactionDBProvider(sharedPebbleCache *pebble.Cache) DBProvider {
	return func(ctx *DBContext) (dbm.DB, error) {
		dbType := dbm.BackendType(ctx.Config.DBBackend)
		path := ctx.Path
		if path == "" {
			path = ctx.Config.DBDir()
		}
		switch dbType {
		case dbm.PebbleDBBackend:
			return dbm.NewPebbleDBWithOpts(ctx.ID, path, buildPebbleOptions(sharedPebbleCache))
		default:
			return dbm.NewDB(ctx.ID, dbType, path)
		}
	}
}

func buildPebbleOptions(sharedCache *pebble.Cache) *pebble.Options {
	o := &pebble.Options{
		MemTableSize:                pebbleMemTableSize,
		MemTableStopWritesThreshold: pebbleMemTableStopWritesThreshold,
		L0StopWritesThreshold:       pebbleL0StopWritesThreshold,
		MaxConcurrentCompactions:    func() int { return pebbleMaxConcurrentCompactions },
		Levels:                      buildPebbleLevels(),
	}
	if sharedCache != nil {
		o.Cache = sharedCache
	}
	o.EnsureDefaults()
	return o
}

// buildPebbleLevels returns per-level options whose TargetFileSize starts at
// pebbleL0TargetFileSize, doubles each level and is capped at
// pebbleMaxTargetFileSize. Setting the levels explicitly (rather than leaving
// Levels nil) is what makes EnsureDefaults keep our larger base instead of
// falling back to the 2 MiB default.
func buildPebbleLevels() []pebble.LevelOptions {
	levels := make([]pebble.LevelOptions, pebbleNumLevels)
	target := pebbleL0TargetFileSize
	for i := range levels {
		levels[i].TargetFileSize = target
		if target < pebbleMaxTargetFileSize {
			target *= 2
		}
	}
	return levels
}
