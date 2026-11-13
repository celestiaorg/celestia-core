package commands

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	dbm "github.com/cometbft/cometbft-db"

	cfg "github.com/cometbft/cometbft/config"
	"github.com/cometbft/cometbft/libs/os"
	"github.com/cometbft/cometbft/state"
	"github.com/cometbft/cometbft/store"
)

// MaxRollbackDepth is the deepest rollback allowed without --force.
const MaxRollbackDepth = 100

var (
	removeBlock    = false
	rollbackHeight int64
	rollbackForce  bool
)

func init() {
	RollbackStateCmd.Flags().BoolVar(&removeBlock, "hard", false, "remove last block as well as state")
	RollbackStateCmd.Flags().Int64Var(&rollbackHeight, "height", 0, "height to roll back to (0 = current height - 1)")
	RollbackStateCmd.Flags().BoolVar(&rollbackForce, "force", false,
		fmt.Sprintf("allow rolling back more than %d heights", MaxRollbackDepth))
}

var RollbackStateCmd = &cobra.Command{
	Use:   "rollback",
	Short: "rollback CometBFT state to a given height",
	Long: fmt.Sprintf(`
A state rollback is performed to recover from an incorrect application state transition,
when CometBFT has persisted an incorrect app hash and is thus unable to make
progress. Rollback overwrites the state with the state at --height (default: current
height - 1). The application should roll back to the same height. Blocks above
--height + 1 are removed; --hard also removes block --height + 1 and allows one height
only. Rolling back more than %d heights requires --force.
`, MaxRollbackDepth),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runRollback(config, rollbackHeight, removeBlock, rollbackForce)
	},
}

func runRollback(config *cfg.Config, height int64, removeBlock, force bool) error {
	target, err := LoadRollbackTarget(config, height)
	if err != nil {
		return fmt.Errorf("failed to rollback state: %w", err)
	}
	fmt.Println(RollbackSummary(target, removeBlock))
	if err := CheckRollbackDepth(target, force); err != nil {
		return err
	}

	// Runs the full check before writing.
	height, hash, err := RollbackStateToHeight(config, target.TargetHeight, removeBlock)
	if err != nil {
		return fmt.Errorf("failed to rollback state: %w", err)
	}

	if removeBlock {
		fmt.Printf("Rolled back both state and block to height %d and hash %X\n", height, hash)
	} else {
		fmt.Printf("Rolled back state to height %d and hash %X\n", height, hash)
	}

	return nil
}

// RollbackTarget holds the store heights and the resolved rollback target.
type RollbackTarget struct {
	BlockBase, BlockHeight, StateHeight, TargetHeight int64
}

// LoadRollbackTarget resolves height (0 = current height - 1) and checks only its range, so it is cheap.
func LoadRollbackTarget(config *cfg.Config, height int64) (RollbackTarget, error) {
	blockStore, stateStore, err := loadStateAndBlockStore(config)
	if err != nil {
		return RollbackTarget{}, err
	}
	defer func() {
		_ = blockStore.Close()
		_ = stateStore.Close()
	}()

	st, err := stateStore.Load()
	if err != nil {
		return RollbackTarget{}, err
	}
	if st.IsEmpty() {
		return RollbackTarget{}, errors.New("no state found")
	}
	if height == 0 {
		if height, err = state.DefaultRollbackHeight(blockStore, stateStore); err != nil {
			return RollbackTarget{}, err
		}
	}

	target := RollbackTarget{
		BlockBase:    blockStore.Base(),
		BlockHeight:  blockStore.Height(),
		StateHeight:  st.LastBlockHeight,
		TargetHeight: height,
	}
	if height < target.BlockBase || height > target.StateHeight {
		return RollbackTarget{}, fmt.Errorf("target height %d out of range [%d, %d]",
			height, target.BlockBase, target.StateHeight)
	}
	return target, nil
}

// RollbackSummary returns a one-line description of the rollback.
func RollbackSummary(target RollbackTarget, removeBlock bool) string {
	keep := target.TargetHeight + 1
	if removeBlock {
		keep = target.TargetHeight
	}
	return fmt.Sprintf("rollback: height %d -> %d, blocks to delete: %d",
		target.StateHeight, target.TargetHeight, max(target.BlockHeight-keep, 0))
}

// CheckRollbackDepth refuses rollbacks deeper than MaxRollbackDepth unless force is set.
func CheckRollbackDepth(target RollbackTarget, force bool) error {
	if depth := target.StateHeight - target.TargetHeight; depth > MaxRollbackDepth && !force {
		return fmt.Errorf("rollback depth %d exceeds %d; use --force",
			depth, MaxRollbackDepth)
	}
	return nil
}

// RollbackState takes the state at the current height n and overwrites it with the state
// at height n - 1. Note state here refers to CometBFT state not application state.
// Returns the latest state height and app hash alongside an error if there was one.
func RollbackState(config *cfg.Config, removeBlock bool) (int64, []byte, error) {
	// use the parsed config to load the block and state store
	blockStore, stateStore, err := loadStateAndBlockStore(config)
	if err != nil {
		return -1, nil, err
	}
	defer func() {
		_ = blockStore.Close()
		_ = stateStore.Close()
	}()

	// rollback the last state
	return state.Rollback(blockStore, stateStore, removeBlock)
}

// CheckRollbackState checks, without writing, that state can be rolled back to height (0 = current height - 1).
func CheckRollbackState(config *cfg.Config, height int64, removeBlock bool) (state.RollbackInfo, error) {
	blockStore, stateStore, err := loadStateAndBlockStore(config)
	if err != nil {
		return state.RollbackInfo{}, err
	}
	defer func() {
		_ = blockStore.Close()
		_ = stateStore.Close()
	}()

	if height == 0 {
		if height, err = state.DefaultRollbackHeight(blockStore, stateStore); err != nil {
			return state.RollbackInfo{}, err
		}
	}
	return state.CheckRollbackToHeight(blockStore, stateStore, height, removeBlock)
}

// RollbackStateToHeight rolls state back to height and returns the new height and app hash.
func RollbackStateToHeight(config *cfg.Config, height int64, removeBlock bool) (int64, []byte, error) {
	blockStore, stateStore, err := loadStateAndBlockStore(config)
	if err != nil {
		return -1, nil, err
	}
	defer func() {
		_ = blockStore.Close()
		_ = stateStore.Close()
	}()

	return state.RollbackToHeight(blockStore, stateStore, height, removeBlock)
}

func loadStateAndBlockStore(config *cfg.Config) (*store.BlockStore, state.Store, error) {
	dbType := dbm.BackendType(config.DBBackend)

	if !os.FileExists(filepath.Join(config.BlockstoreDir(), "blockstore.db")) {
		return nil, nil, fmt.Errorf("no blockstore found in %v", config.BlockstoreDir())
	}

	// Get BlockStore
	blockStoreDB, err := dbm.NewDB("blockstore", dbType, config.BlockstoreDir())
	if err != nil {
		return nil, nil, err
	}
	blockStore := store.NewBlockStore(blockStoreDB)

	if !os.FileExists(filepath.Join(config.DBDir(), "state.db")) {
		return nil, nil, fmt.Errorf("no statestore found in %v", config.DBDir())
	}

	// Get StateStore
	stateDB, err := dbm.NewDB("state", dbType, config.DBDir())
	if err != nil {
		return nil, nil, err
	}
	stateStore := state.NewStore(stateDB, state.StoreOptions{
		DiscardABCIResponses: config.Storage.DiscardABCIResponses,
	})

	return blockStore, stateStore, nil
}
