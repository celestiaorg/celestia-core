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
	if ctx.ID == "blockstore" {
		// Blockstore memtable options: SaveBlock writes a whole block as one batch.
		memtable := ctx.Config.Storage.BlockstoreMemtableSizeMB << 20
		switch dbType {
		case dbm.GoLevelDBBackend:
			return dbm.NewGoLevelDBWithOpts(ctx.ID, path, &opt.Options{
				WriteBuffer:                  memtable,
				DisableLargeBatchTransaction: ctx.Config.Storage.BlockstoreGoLevelDBDisableLargeBatchTransaction,
			})
		case dbm.PebbleDBBackend:
			return dbm.NewPebbleDBWithOpts(ctx.ID, path, &pebble.Options{MemTableSize: uint64(memtable)})
		}
	}
	return dbm.NewDB(ctx.ID, dbType, path)
}
