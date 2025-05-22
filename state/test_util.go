package state

import (
	"os"
	"testing"

	"github.com/cometbft/cometbft/internal/test"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	dbm "github.com/cometbft/cometbft-db"
)

func SetupTestCase(t *testing.T) (func(t *testing.T), dbm.DB, State) {
	config := test.ResetTestRoot("state_")
	dbType := dbm.BackendType(config.DBBackend)
	stateDB, err := dbm.NewDB("state", dbType, config.DBDir())
	stateStore := NewStore(stateDB, StoreOptions{
		DiscardABCIResponses: false,
	})
	require.NoError(t, err)
	state, err := stateStore.LoadFromDBOrGenesisFile(config.GenesisFile())
	assert.NoError(t, err, "expected no error on LoadStateFromDBOrGenesisFile")
	err = stateStore.Save(state)
	require.NoError(t, err)

	tearDown := func(t *testing.T) { os.RemoveAll(config.RootDir) }

	return tearDown, stateDB, state
}
