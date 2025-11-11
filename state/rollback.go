package state

import (
	"errors"
	"fmt"

	cmtstate "github.com/cometbft/cometbft/proto/tendermint/state"
	cmtversion "github.com/cometbft/cometbft/proto/tendermint/version"
	"github.com/cometbft/cometbft/types"
	"github.com/cometbft/cometbft/version"
)

// Rollback overwrites the current CometBFT state (height n) with the most
// recent previous state (height n - 1).
// Note that this function does not affect application state.
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

// RollbackToHeight rolls back CometBFT state to a specific target height.
// Unlike Rollback() which only goes back one block (n -> n-1), this function
// can rollback multiple blocks to reach the target height.
//
// This is useful for recovery scenarios where the application state is ahead
// of the blockstore (e.g., after a crash during batch block saves).
//
// Parameters:
//   - bs: BlockStore to verify blocks exist at target height
//   - ss: State store to save rolled back state
//   - targetHeight: The height to rollback to
//   - removeBlocks: If true, removes blocks after targetHeight from blockstore
//
// Returns the target height and app hash, or an error if rollback fails.
func RollbackToHeight(bs BlockStore, ss Store, targetHeight int64, removeBlocks bool) (int64, []byte, error) {
	if targetHeight <= 0 {
		return -1, nil, fmt.Errorf("target height must be greater than 0, got %d", targetHeight)
	}

	currentState, err := ss.Load()
	if err != nil {
		return -1, nil, fmt.Errorf("failed to load current state: %w", err)
	}
	if currentState.IsEmpty() {
		return -1, nil, errors.New("no state found")
	}

	currentHeight := currentState.LastBlockHeight
	if targetHeight >= currentHeight {
		return -1, nil, fmt.Errorf("target height (%d) must be less than current height (%d)", targetHeight, currentHeight)
	}

	// Verify the target block exists
	targetBlock := bs.LoadBlockMeta(targetHeight)
	if targetBlock == nil {
		return -1, nil, fmt.Errorf("block at target height %d not found", targetHeight)
	}

	// Verify the block at targetHeight+1 exists (needed for AppHash and LastResultsHash)
	nextBlock := bs.LoadBlockMeta(targetHeight + 1)
	if nextBlock == nil {
		return -1, nil, fmt.Errorf("block at height %d not found", targetHeight+1)
	}

	// Load validator set at target height
	validators, err := ss.LoadValidators(targetHeight)
	if err != nil {
		return -1, nil, fmt.Errorf("failed to load validators at height %d: %w", targetHeight, err)
	}

	// Load next validators (at targetHeight+1)
	nextValidators, err := ss.LoadValidators(targetHeight + 1)
	if err != nil {
		return -1, nil, fmt.Errorf("failed to load next validators at height %d: %w", targetHeight+1, err)
	}

	// Load previous validator set (at targetHeight-1) for LastValidators
	var previousValidators *types.ValidatorSet
	if targetHeight > 1 {
		previousValidators, err = ss.LoadValidators(targetHeight - 1)
		if err != nil {
			return -1, nil, fmt.Errorf("failed to load previous validators at height %d: %w", targetHeight-1, err)
		}
	} else {
		// At height 1, previous validators are the same as current
		previousValidators = validators
	}

	// Load consensus params at targetHeight+1
	consensusParams, err := ss.LoadConsensusParams(targetHeight + 1)
	if err != nil {
		return -1, nil, fmt.Errorf("failed to load consensus params at height %d: %w", targetHeight+1, err)
	}

	// Determine when validators last changed
	valChangeHeight := currentState.LastHeightValidatorsChanged
	if valChangeHeight > targetHeight+1 {
		valChangeHeight = targetHeight + 1
	}

	// Determine when consensus params last changed
	paramsChangeHeight := currentState.LastHeightConsensusParamsChanged
	if paramsChangeHeight > targetHeight+1 {
		paramsChangeHeight = targetHeight + 1
	}

	// Build the rolled back state
	rolledBackState := State{
		Version: cmtstate.Version{
			Consensus: cmtversion.Consensus{
				Block: version.BlockProtocol,
				App:   consensusParams.Version.App,
			},
			Software: version.TMCoreSemVer,
		},
		// Immutable fields
		ChainID:       currentState.ChainID,
		InitialHeight: currentState.InitialHeight,

		// Set to target height
		LastBlockHeight: targetBlock.Header.Height,
		LastBlockID:     targetBlock.BlockID,
		LastBlockTime:   targetBlock.Header.Time,

		// Validators
		NextValidators:              nextValidators,
		Validators:                  validators,
		LastValidators:              previousValidators,
		LastHeightValidatorsChanged: valChangeHeight,

		// Consensus params
		ConsensusParams:                  consensusParams,
		LastHeightConsensusParamsChanged: paramsChangeHeight,

		// App hash and results from the next block's header
		// (block N+1 commits the results of block N)
		LastResultsHash: nextBlock.Header.LastResultsHash,
		AppHash:         nextBlock.Header.AppHash,
	}

	// Save the rolled back state
	if err := ss.Save(rolledBackState); err != nil {
		return -1, nil, fmt.Errorf("failed to save rolled back state: %w", err)
	}

	// Remove blocks after target height if requested
	if removeBlocks {
		// Delete blocks from targetHeight+1 to current height
		blocksToRemove := currentHeight - targetHeight
		for i := int64(0); i < blocksToRemove; i++ {
			if err := bs.DeleteLatestBlock(); err != nil {
				return -1, nil, fmt.Errorf("failed to remove block at height %d: %w", currentHeight-i, err)
			}
		}
	}

	return rolledBackState.LastBlockHeight, rolledBackState.AppHash, nil
}
