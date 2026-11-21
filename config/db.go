package config

import (
	"context"

	"github.com/cockroachdb/pebble"
	dbm "github.com/cometbft/cometbft-db"
	"github.com/syndtr/goleveldb/leveldb/opt"

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
// specified in the Config.
func DefaultDBProvider(ctx *DBContext) (dbm.DB, error) {
	dbType := dbm.BackendType(ctx.Config.DBBackend)
	path := ctx.Path
	if path == "" {
		path = ctx.Config.DBDir()
	}
	if ctx.ID == "blockstore" && ctx.Config.Storage != nil {
		return newBlockstoreDB(dbType, path, ctx.Config.Storage)
	}
	return dbm.NewDB(ctx.ID, dbType, path)
}

// newBlockstoreDB opens the blockstore with the [storage] memtable options, so
// SaveBlock's whole-block batch does not stall on level-0 compaction.
func newBlockstoreDB(backend dbm.BackendType, dir string, cfg *StorageConfig) (dbm.DB, error) {
	switch backend {
	case dbm.GoLevelDBBackend:
		return dbm.NewGoLevelDBWithOpts("blockstore", dir, blockstoreGoLevelDBOptions(cfg))
	case dbm.PebbleDBBackend:
		return dbm.NewPebbleDBWithOpts("blockstore", dir, blockstorePebbleOptions(cfg))
	}
	return dbm.NewDB("blockstore", backend, dir)
}

func blockstoreGoLevelDBOptions(cfg *StorageConfig) *opt.Options {
	return &opt.Options{
		WriteBuffer:                  cfg.BlockstoreMemtableSizeMB << 20,
		DisableLargeBatchTransaction: cfg.BlockstoreGoLevelDBDisableLargeBatchTransaction,
	}
}

func blockstorePebbleOptions(cfg *StorageConfig) *pebble.Options {
	return &pebble.Options{MemTableSize: uint64(cfg.BlockstoreMemtableSizeMB) << 20}
}
