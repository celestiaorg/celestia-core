package config

import (
	"context"
	"runtime"

	"github.com/cockroachdb/pebble"
	"github.com/syndtr/goleveldb/leveldb/opt"

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
// compaction is disabled, and by short-lived tools (inspect, light client)
// regardless of compaction settings.
func DefaultDBProvider(ctx *DBContext) (dbm.DB, error) {
	dbType := dbm.BackendType(ctx.Config.DBBackend)
	path := ctx.Path
	if path == "" {
		path = ctx.Config.DBDir()
	}
	return dbm.NewDB(ctx.ID, dbType, path)
}

// Hardcoded DB-tuning values applied only when Storage.Compact is enabled.
// See celestiaorg/celestia-core#3053: with compaction off, the library
// defaults are fine; with compaction on, the default memtable/L0/cache sizes
// let writes stall while a compaction is in flight. These knobs absorb write
// bursts and keep block-save latency bounded under load.
const (
	// PebbleSharedCacheBytes is the size of the shared pebble block cache
	// installed across blockstore, state, evidence, and tx_index when
	// compaction is enabled and the backend is pebbledb.
	PebbleSharedCacheBytes int64 = 1 << 30 // 1 GiB

	pebbleMemTableSize                uint64 = 128 << 20 // 128 MiB
	pebbleMemTableStopWritesThreshold int    = 4
	pebbleL0StopWritesThreshold       int    = 24

	goLevelDBWriteBuffer            = 128 << 20 // 128 MiB
	goLevelDBBlockCacheCapacity     = 512 << 20 // 512 MiB per-DB (goleveldb cannot share)
	goLevelDBWriteL0SlowdownTrigger = 16
	goLevelDBWriteL0PauseTrigger    = 24
)

// pebbleMaxConcurrentCompactions depends on GOMAXPROCS, so it is computed at
// init rather than declared as a const.
var pebbleMaxConcurrentCompactions = max(2, runtime.GOMAXPROCS(0)/2)

// NewCompactionDBProvider returns a DBProvider that applies the
// compaction-friendly tuning above. If sharedPebbleCache is non-nil, it is
// installed into every PebbleDB this provider opens. Pass nil to skip cache
// sharing (each DB gets its own cache).
//
// Callers should only use this provider when Storage.Compact is enabled.
// When compaction is off, the tuning's larger memory footprint is wasteful.
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
		case dbm.GoLevelDBBackend:
			return dbm.NewGoLevelDBWithOpts(ctx.ID, path, buildGoLevelDBOptions())
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
	}
	if sharedCache != nil {
		o.Cache = sharedCache
	}
	o.EnsureDefaults()
	return o
}

func buildGoLevelDBOptions() *opt.Options {
	return &opt.Options{
		WriteBuffer:            goLevelDBWriteBuffer,
		BlockCacheCapacity:     goLevelDBBlockCacheCapacity,
		WriteL0SlowdownTrigger: goLevelDBWriteL0SlowdownTrigger,
		WriteL0PauseTrigger:    goLevelDBWriteL0PauseTrigger,
	}
}
