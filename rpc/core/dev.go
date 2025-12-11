package core

import (
	"fmt"
	"time"

	cs "github.com/cometbft/cometbft/consensus"
	ctypes "github.com/cometbft/cometbft/rpc/core/types"
	rpctypes "github.com/cometbft/cometbft/rpc/jsonrpc/types"
)

// UnsafeFlushMempool removes all transactions from the mempool.
func (env *Environment) UnsafeFlushMempool(*rpctypes.Context) (*ctypes.ResultUnsafeFlushMempool, error) {
	env.Mempool.Flush()
	return &ctypes.ResultUnsafeFlushMempool{}, nil
}

// UnsafeSetConsensusDelay sets configurable delays for consensus phases.
// Duration format: "100ms", "1s", "500us", etc. Empty string = no change.
// Note: precommit delay is not configurable via RPC (handled by app).
func (env *Environment) UnsafeSetConsensusDelay(
	_ *rpctypes.Context,
	proposeDelay *string,
	prevoteDelay *string,
) (*ctypes.ResultUnsafeSetConsensusDelay, error) {
	state, ok := env.ConsensusState.(*cs.State)
	if !ok {
		return nil, fmt.Errorf("consensus state is not of expected type")
	}

	currentPropose, currentPrevote, _ := state.GetConsensusDelays()

	if proposeDelay != nil && *proposeDelay != "" {
		d, err := time.ParseDuration(*proposeDelay)
		if err != nil {
			return nil, fmt.Errorf("invalid propose_delay: %w", err)
		}
		if d < 0 {
			return nil, fmt.Errorf("propose_delay cannot be negative")
		}
		currentPropose = d
	}

	if prevoteDelay != nil && *prevoteDelay != "" {
		d, err := time.ParseDuration(*prevoteDelay)
		if err != nil {
			return nil, fmt.Errorf("invalid prevote_delay: %w", err)
		}
		if d < 0 {
			return nil, fmt.Errorf("prevote_delay cannot be negative")
		}
		currentPrevote = d
	}

	state.SetConsensusDelays(currentPropose, currentPrevote, 0)

	return &ctypes.ResultUnsafeSetConsensusDelay{
		ProposeDelay: currentPropose.String(),
		PrevoteDelay: currentPrevote.String(),
	}, nil
}

// UnsafeGetConsensusDelay returns current consensus phase delays.
// Note: precommit delay is not configurable via RPC (handled by app).
func (env *Environment) UnsafeGetConsensusDelay(
	_ *rpctypes.Context,
) (*ctypes.ResultUnsafeGetConsensusDelay, error) {
	state, ok := env.ConsensusState.(*cs.State)
	if !ok {
		return nil, fmt.Errorf("consensus state is not of expected type")
	}

	propose, prevote, _ := state.GetConsensusDelays()

	return &ctypes.ResultUnsafeGetConsensusDelay{
		ProposeDelay: propose.String(),
		PrevoteDelay: prevote.String(),
	}, nil
}
