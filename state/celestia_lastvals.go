package state

import (
	"bytes"

	abci "github.com/cometbft/cometbft/abci/types"
	"github.com/cometbft/cometbft/types"
)

// lastCommitInfoForApply builds DecidedLastCommit from state.LastValidators,
// which is the validator set of height block.Height-1, instead of loading it
// from the store. It falls back to the store when the guards do not hold.
func lastCommitInfoForApply(state State, block *types.Block, store Store) abci.CommitInfo {
	if lastValidatorsUsable(state, block) {
		return BuildLastCommitInfo(block, state.LastValidators, state.InitialHeight)
	}
	return buildLastCommitInfoFromStore(block, store, state.InitialHeight)
}

// lastValidatorsUsable reports whether state.LastValidators is the set that
// signed block.LastCommit: heights line up, sizes match and every non-absent
// signature's address matches by index.
func lastValidatorsUsable(state State, block *types.Block) bool {
	if block.Height == state.InitialHeight || state.LastBlockHeight != block.Height-1 {
		return false
	}
	lv := state.LastValidators
	if lv == nil || block.LastCommit == nil || len(lv.Validators) == 0 ||
		len(lv.Validators) != len(block.LastCommit.Signatures) {
		return false
	}
	for i, cs := range block.LastCommit.Signatures {
		if cs.BlockIDFlag == types.BlockIDFlagAbsent {
			continue
		}
		if lv.Validators[i] == nil || !bytes.Equal(lv.Validators[i].Address, cs.ValidatorAddress) {
			return false
		}
	}
	return true
}
