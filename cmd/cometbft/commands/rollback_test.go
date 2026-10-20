package commands

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	dbm "github.com/cometbft/cometbft-db"

	cfg "github.com/cometbft/cometbft/config"
	"github.com/cometbft/cometbft/crypto/tmhash"
	cmtversion "github.com/cometbft/cometbft/proto/tendermint/version"
	"github.com/cometbft/cometbft/state"
	"github.com/cometbft/cometbft/store"
	"github.com/cometbft/cometbft/types"
	"github.com/cometbft/cometbft/version"
)

func TestRollbackFlags(t *testing.T) {
	t.Cleanup(func() { rollbackHeight, removeBlock, rollbackForce = 0, false, false })

	require.NoError(t, RollbackStateCmd.ParseFlags([]string{"--height", "7", "--hard", "--force"}))
	require.Equal(t, int64(7), rollbackHeight)
	require.True(t, removeBlock)
	require.True(t, rollbackForce)
}

func TestRollbackBlockNotExecuted(t *testing.T) {
	config := newRollbackConfig(t)
	setupRollbackStores(t, config, 5, 6)

	err := runRollback(config, 0, false, false)
	require.ErrorIs(t, err, state.ErrBlockNotExecuted)
	_, err = CheckRollbackState(config, 0, false)
	require.ErrorIs(t, err, state.ErrBlockNotExecuted)
	requireStoreHeights(t, config, 6, 5)
}

func TestCheckRollbackState(t *testing.T) {
	config := newRollbackConfig(t)
	setupRollbackStores(t, config, 5, 5)

	info, err := CheckRollbackState(config, 0, false)
	require.NoError(t, err)
	require.Equal(t, int64(4), info.TargetHeight)
	// Header h has app hash tmhash.Sum([]byte{h}).
	require.Equal(t, tmhash.Sum([]byte{5}), info.TargetAppHash)
	requireStoreHeights(t, config, 5, 5)
}

func TestRollbackCustomBlockstoreDir(t *testing.T) {
	config := newRollbackConfig(t)
	config.BlockstorePath = "blockstore"
	setupRollbackStores(t, config, 5, 5)
	require.NoDirExists(t, filepath.Join(config.DBDir(), "blockstore.db"))

	require.NoError(t, runRollback(config, 0, false, false))
	requireStoreHeights(t, config, 5, 4)
}

func TestRollbackDepthGuard(t *testing.T) {
	const n = MaxRollbackDepth + 3
	config := newRollbackConfig(t)
	setupRollbackStores(t, config, n, n)

	target, err := LoadRollbackTarget(config, n-MaxRollbackDepth)
	require.NoError(t, err)
	require.NoError(t, CheckRollbackDepth(target, false))
	require.Equal(t, "rollback: height 103 -> 3, blocks to delete: 99", RollbackSummary(target, false))
	require.Equal(t, "rollback: height 103 -> 102, blocks to delete: 1",
		RollbackSummary(RollbackTarget{BlockBase: 1, BlockHeight: n, StateHeight: n, TargetHeight: n - 1}, true))

	_, err = LoadRollbackTarget(config, n+1)
	require.ErrorContains(t, err, "out of range")

	err = runRollback(config, n-MaxRollbackDepth-1, false, false)
	require.ErrorContains(t, err, "use --force")
	requireStoreHeights(t, config, n, n)

	require.NoError(t, runRollback(config, n-MaxRollbackDepth-1, false, true))
	requireStoreHeights(t, config, n-MaxRollbackDepth, n-MaxRollbackDepth-1)
}

func TestRollbackDepthGuardRunsBeforeFullCheck(t *testing.T) {
	const n = MaxRollbackDepth + 3
	config := newRollbackConfig(t)
	setupRollbackStores(t, config, n, n)

	// Prune old states so the full check fails.
	stateDB, err := dbm.NewDB("state", dbm.BackendType(config.DBBackend), config.DBDir())
	require.NoError(t, err)
	stateStore := state.NewStore(stateDB, state.StoreOptions{})
	_, err = stateStore.PruneStates(1, 50, 50, 0)
	require.NoError(t, err)
	require.NoError(t, stateStore.Close())

	err = runRollback(config, 2, false, false)
	require.ErrorContains(t, err, "use --force")

	err = runRollback(config, 2, false, true)
	require.ErrorContains(t, err, "validator set at height 2")
	requireStoreHeights(t, config, n, n)
}

func newRollbackConfig(t *testing.T) *cfg.Config {
	config := cfg.DefaultConfig()
	config.SetRoot(t.TempDir())
	return config
}

// setupRollbackStores writes minimal blocks 1..blockHeight and states up to stateHeight.
func setupRollbackStores(t *testing.T, config *cfg.Config, stateHeight, blockHeight int64) {
	t.Helper()
	dbType := dbm.BackendType(config.DBBackend)
	blockStoreDB, err := dbm.NewDB("blockstore", dbType, config.BlockstoreDir())
	require.NoError(t, err)
	stateDB, err := dbm.NewDB("state", dbType, config.DBDir())
	require.NoError(t, err)
	blockStore := store.NewBlockStore(blockStoreDB)
	stateStore := state.NewStore(stateDB, state.StoreOptions{})
	defer func() {
		require.NoError(t, blockStore.Close())
		require.NoError(t, stateStore.Close())
	}()

	valSet, _ := types.RandValidatorSet(1, 10)
	st := state.State{
		ChainID:                          "test-chain",
		InitialHeight:                    1,
		LastValidators:                   valSet,
		Validators:                       valSet,
		NextValidators:                   valSet,
		LastHeightValidatorsChanged:      1,
		ConsensusParams:                  *types.DefaultConsensusParams(),
		LastHeightConsensusParamsChanged: 1,
	}
	require.NoError(t, stateStore.Save(st))

	for h := int64(1); h <= blockHeight; h++ {
		block := &types.Block{
			Header: types.Header{
				Version:         cmtversion.Consensus{Block: version.BlockProtocol},
				ChainID:         st.ChainID,
				Height:          h,
				AppHash:         tmhash.Sum([]byte{byte(h)}),
				ValidatorsHash:  valSet.Hash(),
				ProposerAddress: valSet.GetProposer().Address,
			},
			LastCommit: &types.Commit{Height: h - 1},
		}
		parts, err := block.MakePartSet(types.BlockPartSizeBytes)
		require.NoError(t, err)
		blockStore.SaveBlock(block, parts, &types.Commit{Height: h})

		if h <= stateHeight {
			st.LastBlockHeight = h
			st.LastBlockID = types.BlockID{Hash: block.Hash(), PartSetHeader: parts.Header()}
			st.AppHash = tmhash.Sum([]byte{byte(h + 1)})
			require.NoError(t, stateStore.Save(st))
		}
	}
}

func requireStoreHeights(t *testing.T, config *cfg.Config, blockHeight, stateHeight int64) {
	t.Helper()
	blockStore, stateStore, err := loadStateAndBlockStore(config)
	require.NoError(t, err)
	defer func() {
		_ = blockStore.Close()
		_ = stateStore.Close()
	}()

	st, err := stateStore.Load()
	require.NoError(t, err)
	require.Equal(t, blockHeight, blockStore.Height())
	require.Equal(t, stateHeight, st.LastBlockHeight)
}
