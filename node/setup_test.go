package node

import (
	"testing"

	dbm "github.com/cometbft/cometbft-db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	cfg "github.com/cometbft/cometbft/config"
)

func TestInitDBs(t *testing.T) {
	testCases := []struct {
		name           string
		blockstorePath string
		expectSamePath bool
	}{
		{
			name:           "custom blockstore path",
			blockstorePath: "blockstore",
			expectSamePath: false,
		},
		{
			name:           "default blockstore path",
			blockstorePath: "", // Should default to DBPath
			expectSamePath: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			config := cfg.DefaultConfig()
			config.SetRoot(t.TempDir())
			config.DBPath = "data"
			config.BlockstorePath = tc.blockstorePath

			// Create a custom DBProvider that tracks which paths were used
			paths := make(map[string]string)
			dbProvider := func(ctx *cfg.DBContext) (dbm.DB, error) {
				paths[ctx.ID] = ctx.Path
				return cfg.DefaultDBProvider(ctx)
			}

			blockStore, stateDB, err := initDBs(config, dbProvider)
			require.NoError(t, err)
			defer func() {
				if blockStore != nil {
					blockStore.Close()
				}
				if stateDB != nil {
					stateDB.Close()
				}
			}()

			if tc.expectSamePath {
				assert.Equal(t, paths["state"], paths["blockstore"], "expected state and blockstore to use same path")
				assert.Equal(t, config.DBDir(), paths["blockstore"])
			} else {
				assert.NotEqual(t, paths["state"], paths["blockstore"], "expected state and blockstore to use different paths")
				assert.Equal(t, config.BlockstoreDir(), paths["blockstore"])
				assert.Equal(t, config.DBDir(), paths["state"])
			}
		})
	}
}
