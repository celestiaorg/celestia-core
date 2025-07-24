package consensus

import (
	"testing"

	"github.com/stretchr/testify/assert"

	cfg "github.com/cometbft/cometbft/config"
	"github.com/cometbft/cometbft/consensus/propagation"
	sm "github.com/cometbft/cometbft/state"
	"github.com/cometbft/cometbft/types"
)

func TestCheckAndDisableOldPropagationRoutineUnit(t *testing.T) {
	// Create a minimal consensus state with app version 5
	config := cfg.DefaultConsensusConfig()
	config.DisablePropagationReactor = false

	// Create a minimal state with app version 5
	state := sm.State{
		ConsensusParams: types.ConsensusParams{
			Version: types.VersionParams{
				App: 5,
			},
		},
		LastBlockHeight: 100,
	}

	// Create consensus state
	cs := &State{
		state: state,
	}

	// Create reactor
	propagator := propagation.NewNoOpPropagator()
	reactor := NewReactor(cs, propagator, false, WithConfig(config))

	// Initially gossip data should be enabled
	assert.True(t, reactor.IsGossipDataEnabled())

	// Call the method directly
	reactor.checkAndDisableOldPropagationRoutine()

	// Now gossip data should be disabled
	assert.False(t, reactor.IsGossipDataEnabled())
}

func TestCheckAndDisableOldPropagationRoutineUnitConditions(t *testing.T) {
	t.Run("no config should not disable", func(t *testing.T) {
		// Create consensus state with app version 5
		state := sm.State{
			ConsensusParams: types.ConsensusParams{
				Version: types.VersionParams{
					App: 5,
				},
			},
		}

		cs := &State{
			state: state,
		}

		// Create reactor without config
		propagator := propagation.NewNoOpPropagator()
		reactor := NewReactor(cs, propagator, false)

		// Initially gossip data should be enabled
		assert.True(t, reactor.IsGossipDataEnabled())

		// Call the method directly
		reactor.checkAndDisableOldPropagationRoutine()

		// Should remain enabled (no config)
		assert.True(t, reactor.IsGossipDataEnabled())
	})

	t.Run("new propagation disabled should not disable old", func(t *testing.T) {
		config := cfg.DefaultConsensusConfig()
		config.DisablePropagationReactor = true // New propagation is disabled

		state := sm.State{
			ConsensusParams: types.ConsensusParams{
				Version: types.VersionParams{
					App: 5,
				},
			},
		}

		cs := &State{
			state: state,
		}

		propagator := propagation.NewNoOpPropagator()
		reactor := NewReactor(cs, propagator, false, WithConfig(config))

		assert.True(t, reactor.IsGossipDataEnabled())

		reactor.checkAndDisableOldPropagationRoutine()

		// Should remain enabled (new propagation is disabled)
		assert.True(t, reactor.IsGossipDataEnabled())
	})

	t.Run("app version 4 should not disable", func(t *testing.T) {
		config := cfg.DefaultConsensusConfig()
		config.DisablePropagationReactor = false

		state := sm.State{
			ConsensusParams: types.ConsensusParams{
				Version: types.VersionParams{
					App: 4, // App version 4
				},
			},
		}

		cs := &State{
			state: state,
		}

		propagator := propagation.NewNoOpPropagator()
		reactor := NewReactor(cs, propagator, false, WithConfig(config))

		assert.True(t, reactor.IsGossipDataEnabled())

		reactor.checkAndDisableOldPropagationRoutine()

		// Should remain enabled (app version < 5)
		assert.True(t, reactor.IsGossipDataEnabled())
	})

	t.Run("only checks when app version changes", func(t *testing.T) {
		config := cfg.DefaultConsensusConfig()
		config.DisablePropagationReactor = false

		state := sm.State{
			ConsensusParams: types.ConsensusParams{
				Version: types.VersionParams{
					App: 4, // Start with app version 4
				},
			},
		}

		cs := &State{
			state: state,
		}

		propagator := propagation.NewNoOpPropagator()
		reactor := NewReactor(cs, propagator, false, WithConfig(config))

		assert.True(t, reactor.IsGossipDataEnabled())
		assert.Equal(t, uint64(0), reactor.lastCheckedAppVersion) // Not checked yet

		// First call should check and set lastCheckedAppVersion
		reactor.checkAndDisableOldPropagationRoutine()
		assert.True(t, reactor.IsGossipDataEnabled()) // Still enabled (version 4)
		assert.Equal(t, uint64(4), reactor.lastCheckedAppVersion)

		// Second call with same version should not recheck
		reactor.checkAndDisableOldPropagationRoutine()
		assert.True(t, reactor.IsGossipDataEnabled())
		assert.Equal(t, uint64(4), reactor.lastCheckedAppVersion)

		// Change app version to 5
		cs.state.ConsensusParams.Version.App = 5

		// Now it should check again and disable
		reactor.checkAndDisableOldPropagationRoutine()
		assert.False(t, reactor.IsGossipDataEnabled()) // Now disabled
		assert.Equal(t, uint64(5), reactor.lastCheckedAppVersion)

		// Enable it again to test that subsequent calls don't re-disable
		reactor.gossipDataEnabled.Store(true)
		
		// Call again with same version - should not check or change anything
		reactor.checkAndDisableOldPropagationRoutine()
		assert.True(t, reactor.IsGossipDataEnabled()) // Should remain enabled since already disabled is checked first
	})
}