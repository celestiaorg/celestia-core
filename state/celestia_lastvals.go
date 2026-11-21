package state

import (
	"bytes"
	"os"
	"sync/atomic"

	abci "github.com/cometbft/cometbft/abci/types"
	"github.com/cometbft/cometbft/types"
)

// celestia prototype (gated by CELESTIA_BLOCKSYNC_PIPELINE=1):
// applyBlock builds DecidedLastCommit from state.LastValidators instead of
// re-loading the validator set for height-1 from the state store. The store
// load decodes ~100 validators and, when the set was not saved at that height,
// replays IncrementProposerPriority from the last checkpoint.
//
// BuildLastCommitInfo only reads each validator's address and voting power,
// in order. state.LastValidators is, by construction of updateState, the
// validator set of height state.LastBlockHeight (== block.Height-1), the
// same set that was saved for that height; proposer priorities (the only
// thing the store replay recomputes) are not used. As an extra guard the
// fast path is only taken when the heights line up, the sizes match and every
// non-absent commit signature's validator address matches by index;
// otherwise the original store path is used.
var lastValsFromStateEnabled atomic.Bool

func init() {
	lastValsFromStateEnabled.Store(os.Getenv("CELESTIA_BLOCKSYNC_PIPELINE") == "1")
}

// SetLastValidatorsFromState toggles the prototype at runtime and returns the
// previous value. Intended for tests.
func SetLastValidatorsFromState(enabled bool) bool {
	return lastValsFromStateEnabled.Swap(enabled)
}

var lastValsFromStateHits atomic.Int64

func lastCommitInfoForApply(state State, block *types.Block, store Store) abci.CommitInfo {
	if lastValsFromStateEnabled.Load() && lastValidatorsUsable(state, block) {
		lastValsFromStateHits.Add(1)
		return BuildLastCommitInfo(block, state.LastValidators, state.InitialHeight)
	}
	return buildLastCommitInfoFromStore(block, store, state.InitialHeight)
}

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

// LastValidatorsFromStateHits returns how many times applyBlock used
// state.LastValidators instead of loading the set from the store.
func LastValidatorsFromStateHits() int64 { return lastValsFromStateHits.Load() }
