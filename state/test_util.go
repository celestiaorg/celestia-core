package state

import (
	"os"
	"testing"
	"time"

	"github.com/cometbft/cometbft/internal/test"
	"github.com/cometbft/cometbft/privval"
	"github.com/cometbft/cometbft/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	dbm "github.com/cometbft/cometbft-db"
)

func SetupTestCase(t testing.TB) (func(t testing.TB), dbm.DB, State) {
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

	tearDown := func(t testing.TB) { os.RemoveAll(config.RootDir) }

	return tearDown, stateDB, state
}

// SetupTestCaseWithPrivVal is like SetupTestCase but also returns the private validator
// that matches the genesis validator. This is useful for tests that need to create
// commits matching the validator set.
func SetupTestCaseWithPrivVal(t testing.TB) (func(t testing.TB), dbm.DB, State, types.PrivValidator) {
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

	// Load the private validator that matches the genesis validator
	pv := privval.LoadFilePV(config.PrivValidatorKeyFile(), config.PrivValidatorStateFile())

	tearDown := func(t testing.TB) { os.RemoveAll(config.RootDir) }

	return tearDown, stateDB, state, pv
}

// MakeTestCommit creates a commit for testing.
// This creates an empty commit (with only absent signatures) which is valid for
// state.MakeBlock because:
// - For height == InitialHeight, MedianTime is not called (genesis time is used)
// - For height > InitialHeight, MedianTime iterates over signatures but skips absent ones
//
// This approach is simpler than creating valid signatures because:
// 1. When state.LastBlockHeight == 0, state.LastValidators is empty
// 2. FilePV tracks signed heights and refuses to sign the same height twice
//
// The returned commit has a random BlockID which doesn't matter for these tests
// since we're not validating the commit against an actual previous block.
func MakeTestCommit(_ testing.TB, _ State, _ types.PrivValidator, _ time.Time) *types.Commit {
	// Create a random block ID for the commit
	blockID := types.MakeBlockIDRandom()

	// Return an empty commit - no signatures
	// This works because MedianTime only processes non-absent signatures,
	// and state.MakeBlock uses genesis time when height == InitialHeight
	return &types.Commit{
		Height:     0,
		Round:      0,
		BlockID:    blockID,
		Signatures: []types.CommitSig{},
	}
}
