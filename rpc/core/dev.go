package core

import (
	"fmt"

	ctypes "github.com/cometbft/cometbft/rpc/core/types"
	rpctypes "github.com/cometbft/cometbft/rpc/jsonrpc/types"
)

// UnsafeFlushMempool removes all transactions from the mempool.
func (env *Environment) UnsafeFlushMempool(*rpctypes.Context) (*ctypes.ResultUnsafeFlushMempool, error) {
	env.Mempool.Flush()
	return &ctypes.ResultUnsafeFlushMempool{}, nil
}

// UnsafeDebugHalt halts the consensus state to simulate falling behind for catchup testing.
// This is an unsafe debugging operation that pauses block commits while allowing
// message processing to continue. Other nodes will continue to make progress,
// and this node will need to catch up when unlocked.
//
// Use UnsafeDebugUnlock to resume normal operation.
func (env *Environment) UnsafeDebugHalt(*rpctypes.Context) (*ctypes.ResultUnsafeDebugHalt, error) {
	debugCS, ok := env.ConsensusState.(debugConsensusState)
	if !ok {
		return nil, fmt.Errorf("consensus state does not support debug operations")
	}

	height := debugCS.DebugHalt()

	env.Logger.Info("UnsafeDebugHalt: consensus halted", "height", height)

	return &ctypes.ResultUnsafeDebugHalt{
		Height: height,
		Halted: true,
	}, nil
}

// UnsafeDebugUnlock unlocks the consensus state after a debug halt.
// Returns information about how long the node was halted and the current state.
// The node will then proceed to catch up to the chain tip.
func (env *Environment) UnsafeDebugUnlock(*rpctypes.Context) (*ctypes.ResultUnsafeDebugUnlock, error) {
	debugCS, ok := env.ConsensusState.(debugConsensusState)
	if !ok {
		return nil, fmt.Errorf("consensus state does not support debug operations")
	}

	if !debugCS.IsDebugHalted() {
		return &ctypes.ResultUnsafeDebugUnlock{
			WasHalted: false,
		}, nil
	}

	haltDuration, haltHeight, currentHeight := debugCS.DebugUnlock()

	env.Logger.Info("UnsafeDebugUnlock: consensus unlocked",
		"halt_duration_ns", haltDuration.Nanoseconds(),
		"halt_height", haltHeight,
		"current_height", currentHeight)

	return &ctypes.ResultUnsafeDebugUnlock{
		HaltDurationNs: haltDuration.Nanoseconds(),
		HaltHeight:     haltHeight,
		CurrentHeight:  currentHeight,
		WasHalted:      true,
	}, nil
}
