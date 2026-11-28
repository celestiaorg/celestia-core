package config

import (
	"context"

	"github.com/cockroachdb/pebble"
	"github.com/cockroachdb/pebble/vfs"
	dbm "github.com/cometbft/cometbft-db"

	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/libs/service"
	"github.com/cometbft/cometbft/libs/walfsync"
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
	if dbType == dbm.PebbleDBBackend {
		opts := &pebble.Options{}
		if ctx.Config.Storage.DBWALFsync != walfsync.ModeAlways {
			opts.FS = walfsync.WrapFS(vfs.Default)
		}
		return dbm.NewPebbleDBWithOpts(ctx.ID, path, opts)
	}
	return dbm.NewDB(ctx.ID, dbType, path)
}
