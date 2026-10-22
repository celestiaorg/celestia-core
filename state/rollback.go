package state

import (
	"bytes"
	"errors"
	"fmt"

	cmtstate "github.com/cometbft/cometbft/proto/tendermint/state"
	cmtversion "github.com/cometbft/cometbft/proto/tendermint/version"
	"github.com/cometbft/cometbft/version"
)

// Rollback overwrites the current CometBFT state (height n) with the most
// recent previous state (height n - 1).
// Note that this function does not affect application state.
// See RollbackToHeight for a checked multi-height rollback.
func Rollback(bs BlockStore, ss Store, removeBlock bool) (int64, []byte, error) {
	invalidState, err := ss.Load()
	if err != nil {
		return -1, nil, err
	}
	if invalidState.IsEmpty() {
		return -1, nil, errors.New("no state found")
	}

	height := bs.Height()

	// NOTE: persistence of state and blocks don't happen atomically. Therefore it is possible that
	// when the user stopped the node the state wasn't updated but the blockstore was. Discard the
	// pending block before continuing.
	if height == invalidState.LastBlockHeight+1 {
		if removeBlock {
			if err := bs.DeleteLatestBlock(); err != nil {
				return -1, nil, fmt.Errorf("failed to remove final block from blockstore: %w", err)
			}
		}
		return invalidState.LastBlockHeight, invalidState.AppHash, nil
	}

	// If the state store isn't one below nor equal to the blockstore height than this violates the
	// invariant
	if height != invalidState.LastBlockHeight {
		return -1, nil, fmt.Errorf("statestore height (%d) is not one below or equal to blockstore height (%d)",
			invalidState.LastBlockHeight, height)
	}

	// state store height is equal to blockstore height. We're good to proceed with rolling back state
	rollbackHeight := invalidState.LastBlockHeight - 1
	rollbackBlock := bs.LoadBlockMeta(rollbackHeight)
	if rollbackBlock == nil {
		return -1, nil, fmt.Errorf("block at height %d not found", rollbackHeight)
	}
	// We also need to retrieve the latest block because the app hash and last
	// results hash is only agreed upon in the following block.
	latestBlock := bs.LoadBlockMeta(invalidState.LastBlockHeight)
	if latestBlock == nil {
		return -1, nil, fmt.Errorf("block at height %d not found", invalidState.LastBlockHeight)
	}

	previousLastValidatorSet, err := ss.LoadValidators(rollbackHeight)
	if err != nil {
		return -1, nil, err
	}

	previousParams, err := ss.LoadConsensusParams(rollbackHeight + 1)
	if err != nil {
		return -1, nil, err
	}

	nextHeight := rollbackHeight + 1
	valChangeHeight := invalidState.LastHeightValidatorsChanged
	// this can only happen if the validator set changed since the last block
	if valChangeHeight > nextHeight+1 {
		valChangeHeight = nextHeight + 1
	}

	paramsChangeHeight := invalidState.LastHeightConsensusParamsChanged
	// this can only happen if params changed from the last block
	if paramsChangeHeight > rollbackHeight {
		paramsChangeHeight = rollbackHeight + 1
	}

	// build the new state from the old state and the prior block
	rolledBackState := State{
		Version: cmtstate.Version{
			Consensus: cmtversion.Consensus{
				Block: version.BlockProtocol,
				App:   previousParams.Version.App,
			},
			Software: version.TMCoreSemVer,
		},
		// immutable fields
		ChainID:       invalidState.ChainID,
		InitialHeight: invalidState.InitialHeight,

		LastBlockHeight: rollbackBlock.Header.Height,
		LastBlockID:     rollbackBlock.BlockID,
		LastBlockTime:   rollbackBlock.Header.Time,

		NextValidators:              invalidState.Validators,
		Validators:                  invalidState.LastValidators,
		LastValidators:              previousLastValidatorSet,
		LastHeightValidatorsChanged: valChangeHeight,

		ConsensusParams:                  previousParams,
		LastHeightConsensusParamsChanged: paramsChangeHeight,

		LastResultsHash: latestBlock.Header.LastResultsHash,
		AppHash:         latestBlock.Header.AppHash,
	}

	// persist the new state. This overrides the invalid one. NOTE: this will also
	// persist the validator set and consensus params over the existing structures,
	// but both should be the same
	if err := ss.Save(rolledBackState); err != nil {
		return -1, nil, fmt.Errorf("failed to save rolled back state: %w", err)
	}

	// If removeBlock is true then also remove the block associated with the previous state.
	// This will mean both the last state and last block height is equal to n - 1
	if removeBlock {
		if err := bs.DeleteLatestBlock(); err != nil {
			return -1, nil, fmt.Errorf("failed to remove final block from blockstore: %w", err)
		}
	}

	return rolledBackState.LastBlockHeight, rolledBackState.AppHash, nil
}

// ErrBlockNotExecuted means the latest block was saved but not executed.
var ErrBlockNotExecuted = errors.New("block saved but not executed")

// RollbackInfo describes a checked rollback to TargetHeight.
type RollbackInfo struct {
	BlockBase, BlockHeight, StateHeight, TargetHeight int64
	TargetAppHash                                     []byte // app hash after executing TargetHeight
	CurrentAppVersion, TargetAppVersion               uint64
}

// DefaultRollbackHeight returns the state height minus one, or ErrBlockNotExecuted if the latest block was not executed.
func DefaultRollbackHeight(bs BlockStore, ss Store) (int64, error) {
	state, err := ss.Load()
	if err != nil {
		return -1, err
	}
	if state.IsEmpty() {
		return -1, errors.New("no state found")
	}

	height := bs.Height()
	switch height {
	case state.LastBlockHeight:
		return state.LastBlockHeight - 1, nil
	case state.LastBlockHeight + 1:
		return -1, fmt.Errorf("%w at height %d; use --height %d to keep it or --height %d to roll back state",
			ErrBlockNotExecuted, height, state.LastBlockHeight, state.LastBlockHeight-1)
	default:
		return -1, fmt.Errorf("statestore height (%d) is not one below or equal to blockstore height (%d)",
			state.LastBlockHeight, height)
	}
}

// CheckRollbackToHeight checks, without writing, that state can be rolled back to target.
// Corrupt records may still panic or exit in the store loaders.
func CheckRollbackToHeight(bs BlockStore, ss Store, target int64, removeBlock bool) (RollbackInfo, error) {
	state, err := ss.Load()
	if err != nil {
		return RollbackInfo{}, err
	}
	if state.IsEmpty() {
		return RollbackInfo{}, errors.New("no state found")
	}

	info := RollbackInfo{
		BlockBase:         bs.Base(),
		BlockHeight:       bs.Height(),
		StateHeight:       state.LastBlockHeight,
		TargetHeight:      target,
		CurrentAppVersion: state.Version.Consensus.App,
	}
	if info.BlockHeight != info.StateHeight && info.BlockHeight != info.StateHeight+1 {
		return RollbackInfo{}, fmt.Errorf("statestore height (%d) is not one below or equal to blockstore height (%d)",
			info.StateHeight, info.BlockHeight)
	}
	if target < info.BlockBase || target > info.StateHeight {
		return RollbackInfo{}, fmt.Errorf("target height %d out of range [%d, %d]", target, info.BlockBase, info.StateHeight)
	}
	if removeBlock && info.StateHeight-target > 1 {
		return RollbackInfo{}, errors.New("--hard rolls back at most one height")
	}

	for h := target; h <= info.BlockHeight; h++ {
		meta := bs.LoadBlockMeta(h)
		if meta == nil {
			return RollbackInfo{}, fmt.Errorf("block meta at height %d not found", h)
		}
		if meta.Header.Height != h {
			return RollbackInfo{}, fmt.Errorf("block meta at height %d has height %d", h, meta.Header.Height)
		}
	}
	// Same reads as each Rollback step.
	for h := target; h < info.StateHeight; h++ {
		if _, err := ss.LoadValidators(h); err != nil {
			return RollbackInfo{}, fmt.Errorf("validator set at height %d: %w", h, err)
		}
		if _, err := ss.LoadConsensusParams(h + 1); err != nil {
			return RollbackInfo{}, fmt.Errorf("consensus params at height %d: %w", h+1, err)
		}
	}

	if target == info.StateHeight {
		info.TargetAppHash = state.AppHash
		info.TargetAppVersion = state.Version.Consensus.App
		return info, nil
	}
	params, err := ss.LoadConsensusParams(target + 1)
	if err != nil {
		return RollbackInfo{}, fmt.Errorf("consensus params at height %d: %w", target+1, err)
	}
	info.TargetAppHash = bs.LoadBlockMeta(target + 1).Header.AppHash
	info.TargetAppVersion = params.Version.App
	return info, nil
}

// RollbackToHeight rolls state back to target, deleting blocks above target+1 (and target+1 if removeBlock).
// It is safe to re-run after a crash.
func RollbackToHeight(bs BlockStore, ss Store, target int64, removeBlock bool) (int64, []byte, error) {
	info, err := CheckRollbackToHeight(bs, ss, target, removeBlock)
	if err != nil {
		return -1, nil, err
	}

	// Each step keeps the blockstore at most one height above state.
	for {
		state, err := ss.Load()
		if err != nil {
			return -1, nil, err
		}
		height := bs.Height()

		switch {
		case height == state.LastBlockHeight+1 && (height > target+1 || (removeBlock && height == target+1)):
			if err := bs.DeleteLatestBlock(); err != nil {
				return -1, nil, fmt.Errorf("failed to remove block %d: %w", height, err)
			}
			if bs.Height() != height-1 {
				return -1, nil, fmt.Errorf("blockstore height is %d after removing block %d", bs.Height(), height)
			}
		case state.LastBlockHeight > target:
			newHeight, _, err := Rollback(bs, ss, state.LastBlockHeight-1 > target || removeBlock)
			if err != nil {
				return -1, nil, err
			}
			if newHeight != state.LastBlockHeight-1 {
				return -1, nil, fmt.Errorf("rollback from height %d returned height %d", state.LastBlockHeight, newHeight)
			}
		default:
			if !bytes.Equal(state.AppHash, info.TargetAppHash) {
				return -1, nil, fmt.Errorf("app hash mismatch at height %d: got %X, want %X",
					state.LastBlockHeight, state.AppHash, info.TargetAppHash)
			}
			return state.LastBlockHeight, state.AppHash, nil
		}
	}
}
