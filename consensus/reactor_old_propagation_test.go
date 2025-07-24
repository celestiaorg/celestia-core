package consensus

import (
	"testing"

	"github.com/stretchr/testify/assert"

	abci "github.com/cometbft/cometbft/abci/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cometbft/cometbft/types"
)

func TestReactorDisableOldPropagationRoutineOnAppVersion5(t *testing.T) {
	t.Run("disable old propagation when app version >= 5", func(t *testing.T) {
		// Create a basic reactor
		N := 1
		css, cleanup := randConsensusNet(t, N, "consensus_reactor_old_propagation_test", newMockTickerFunc(true), newKVStore)
		defer cleanup()

		reactor := NewReactor(css[0], css[0].propagator, false)
		defer reactor.Stop()

		// Initially, gossip data should be enabled
		assert.True(t, reactor.IsGossipDataEnabled())

		// Create a mock new block event with app version 5
		blockEvent := types.EventDataNewBlock{
			Block: &types.Block{
				Header: types.Header{Height: 100},
			},
			BlockID: types.BlockID{},
			ResultFinalizeBlock: abci.ResponseFinalizeBlock{
				ConsensusParamUpdates: &cmtproto.ConsensusParams{
					Version: &cmtproto.VersionParams{App: 5},
				},
			},
		}

		// Handle the event directly
		reactor.handleNewBlockEvent(blockEvent)

		// The old propagation routine should now be disabled
		assert.False(t, reactor.IsGossipDataEnabled())
	})

	t.Run("keep old propagation when app version < 5", func(t *testing.T) {
		// Create a basic reactor
		N := 1
		css, cleanup := randConsensusNet(t, N, "consensus_reactor_old_propagation_test", newMockTickerFunc(true), newKVStore)
		defer cleanup()

		reactor := NewReactor(css[0], css[0].propagator, false)
		defer reactor.Stop()

		// Initially, gossip data should be enabled
		assert.True(t, reactor.IsGossipDataEnabled())

		// Create a mock new block event with app version 4
		blockEvent := types.EventDataNewBlock{
			Block: &types.Block{
				Header: types.Header{Height: 100},
			},
			BlockID: types.BlockID{},
			ResultFinalizeBlock: abci.ResponseFinalizeBlock{
				ConsensusParamUpdates: &cmtproto.ConsensusParams{
					Version: &cmtproto.VersionParams{App: 4},
				},
			},
		}

		// Handle the event directly
		reactor.handleNewBlockEvent(blockEvent)

		// The old propagation routine should still be enabled
		assert.True(t, reactor.IsGossipDataEnabled())
	})

	t.Run("no action when already disabled", func(t *testing.T) {
		// Create a basic reactor
		N := 1
		css, cleanup := randConsensusNet(t, N, "consensus_reactor_old_propagation_test", newMockTickerFunc(true), newKVStore)
		defer cleanup()

		reactor := NewReactor(css[0], css[0].propagator, false)
		defer reactor.Stop()

		// Disable gossip data first
		reactor.gossipDataEnabled.Store(false)
		assert.False(t, reactor.IsGossipDataEnabled())

		// Create a mock new block event with app version 5
		blockEvent := types.EventDataNewBlock{
			Block: &types.Block{
				Header: types.Header{Height: 100},
			},
			BlockID: types.BlockID{},
			ResultFinalizeBlock: abci.ResponseFinalizeBlock{
				ConsensusParamUpdates: &cmtproto.ConsensusParams{
					Version: &cmtproto.VersionParams{App: 5},
				},
			},
		}

		// Handle the event directly
		reactor.handleNewBlockEvent(blockEvent)

		// Should still be disabled (no change)
		assert.False(t, reactor.IsGossipDataEnabled())
	})
}