package config

import (
	"context"

	dbm "github.com/cometbft/cometbft-db"

	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/libs/service"
)

// ServiceProvider is a function type that takes configuration and a logger,
// and returns a ready-to-go service.Service instance (e.g., a CometBFT Node).
//
// Arguments:
// - context.Context: The execution context for the service.
// - *Config: The application's main configuration.
// - log.Logger: The logger instance.
// Returns:
// - service.Service: The initialized service.
// - error: An error if initialization fails.
type ServiceProvider func(context.Context, *Config, log.Logger) (service.Service, error)

// DBContext specifies necessary configuration information required for 
// loading and initializing a new database instance.
type DBContext struct {
	ID     string  // Unique identifier for the database instance (e.g., "state", "blockstore").
	Config *Config // Reference to the main application configuration.
	Path   string  // Optional custom path for the database files.
}

// DBProvider is a function type that takes database context and returns an
// instantiated database object. This allows for flexible DB backend selection.
//
// Arguments:
// - *DBContext: Configuration context for the database.
// Returns:
// - dbm.DB: The initialized database instance.
// - error: An error if database creation fails.
type DBProvider func(*DBContext) (dbm.DB, error)

// DefaultDBProvider creates a database instance based on the DBBackend and DBDir
// settings specified in the main application Config.
func DefaultDBProvider(ctx *DBContext) (dbm.DB, error) {
	// Determine the database backend type from the configuration string
	dbType := dbm.BackendType(ctx.Config.DBBackend)
	
	path := ctx.Path
	// Use the configured DBDir if a custom path is not specified
	if path == "" {
		path = ctx.Config.DBDir()
	}
	
	// Instantiate the database using the determined type and path
	return dbm.NewDB(ctx.ID, dbType, path)
}
