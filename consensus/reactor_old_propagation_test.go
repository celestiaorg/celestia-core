package consensus

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	cfg "github.com/cometbft/cometbft/config"
)

func TestReactorDisableOldPropagationRoutineOnAppVersion5(t *testing.T) {
	t.Run("disable old propagation when app version >= 5", func(t *testing.T) {
		N := 1
		css, cleanup := randConsensusNet(t, N, "consensus_reactor_old_propagation_test", newMockTickerFunc(true), newKVStore)
		defer cleanup()

		// Modify the state to have app version 5
		css[0].mtx.Lock()
		css[0].state.ConsensusParams.Version.App = 5
		css[0].mtx.Unlock()

		// Create test configuration with new propagation reactor enabled
		consensusConfig := cfg.DefaultConsensusConfig()
		consensusConfig.DisablePropagationReactor = false

		// Create reactor with the test config
		reactor := NewReactor(css[0], css[0].propagator, false, WithConfig(consensusConfig))
		defer reactor.Stop()

		// Initially, gossip data should be enabled
		assert.True(t, reactor.IsGossipDataEnabled())

		// Start the reactor to trigger the update routine
		err := reactor.Start()
		require.NoError(t, err)

		// Wait a bit for the update routine to run
		time.Sleep(200 * time.Millisecond)

		// The old propagation routine should now be disabled
		assert.False(t, reactor.IsGossipDataEnabled())
	})

	t.Run("keep old propagation when app version < 5", func(t *testing.T) {
		N := 1
		css, cleanup := randConsensusNet(t, N, "consensus_reactor_old_propagation_test", newMockTickerFunc(true), newKVStore)
		defer cleanup()

		// Modify the state to have app version 4
		css[0].mtx.Lock()
		css[0].state.ConsensusParams.Version.App = 4
		css[0].mtx.Unlock()

		// Create test configuration with new propagation reactor enabled
		consensusConfig := cfg.DefaultConsensusConfig()
		consensusConfig.DisablePropagationReactor = false

		// Create reactor with the test config
		reactor := NewReactor(css[0], css[0].propagator, false, WithConfig(consensusConfig))
		defer reactor.Stop()

		// Initially, gossip data should be enabled
		assert.True(t, reactor.IsGossipDataEnabled())

		// Start the reactor to trigger the update routine
		err := reactor.Start()
		require.NoError(t, err)

		// Wait a bit for the update routine to run
		time.Sleep(200 * time.Millisecond)

		// The old propagation routine should still be enabled
		assert.True(t, reactor.IsGossipDataEnabled())
	})

	t.Run("keep old propagation when new propagation reactor is disabled", func(t *testing.T) {
		N := 1
		css, cleanup := randConsensusNet(t, N, "consensus_reactor_old_propagation_test", newMockTickerFunc(true), newKVStore)
		defer cleanup()

		// Modify the state to have app version 5
		css[0].mtx.Lock()
		css[0].state.ConsensusParams.Version.App = 5
		css[0].mtx.Unlock()

		// Create test configuration with new propagation reactor disabled
		consensusConfig := cfg.DefaultConsensusConfig()
		consensusConfig.DisablePropagationReactor = true

		// Create reactor with the test config
		reactor := NewReactor(css[0], css[0].propagator, false, WithConfig(consensusConfig))
		defer reactor.Stop()

		// Initially, gossip data should be enabled
		assert.True(t, reactor.IsGossipDataEnabled())

		// Start the reactor to trigger the update routine
		err := reactor.Start()
		require.NoError(t, err)

		// Wait a bit for the update routine to run
		time.Sleep(200 * time.Millisecond)

		// The old propagation routine should still be enabled since new propagation is disabled
		assert.True(t, reactor.IsGossipDataEnabled())
	})

	t.Run("no action when old propagation already disabled", func(t *testing.T) {
		N := 1
		css, cleanup := randConsensusNet(t, N, "consensus_reactor_old_propagation_test", newMockTickerFunc(true), newKVStore)
		defer cleanup()

		// Modify the state to have app version 5
		css[0].mtx.Lock()
		css[0].state.ConsensusParams.Version.App = 5
		css[0].mtx.Unlock()

		// Create test configuration with new propagation reactor enabled
		consensusConfig := cfg.DefaultConsensusConfig()
		consensusConfig.DisablePropagationReactor = false

		// Create reactor with the test config and gossip data disabled
		reactor := NewReactor(css[0], css[0].propagator, false, WithConfig(consensusConfig), WithGossipDataEnabled(false))
		defer reactor.Stop()

		// Initially, gossip data should be disabled
		assert.False(t, reactor.IsGossipDataEnabled())

		// Start the reactor to trigger the update routine
		err := reactor.Start()
		require.NoError(t, err)

		// Wait a bit for the update routine to run
		time.Sleep(200 * time.Millisecond)

		// The old propagation routine should remain disabled
		assert.False(t, reactor.IsGossipDataEnabled())
	})
}