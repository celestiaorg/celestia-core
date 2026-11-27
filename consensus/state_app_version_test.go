package consensus

import (
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/consensus/propagation"
)

// appVersionRecorder records the last app version consensus installs on the
// propagator.
type appVersionRecorder struct {
	*propagation.NoOpPropagator
	appVersion atomic.Uint64
}

func (r *appVersionRecorder) SetAppVersion(appVersion uint64) {
	r.appVersion.Store(appVersion)
}

// TestUpdateToStateRefreshesPropagatorAppVersion ensures that an app version
// change in state reaches the propagation reactor when consensus moves to the
// next height, so it switches compact block signature format without a
// restart.
func TestUpdateToStateRefreshesPropagatorAppVersion(t *testing.T) {
	cs, _ := randState(1)
	rec := &appVersionRecorder{NoOpPropagator: propagation.NewNoOpPropagator()}
	cs.propagator = rec

	const upgradedAppVersion uint64 = 12
	next := cs.state.Copy()
	next.LastBlockHeight = cs.state.LastBlockHeight + 1
	next.Version.Consensus.App = upgradedAppVersion
	// updateToState requires a last commit after the initial height.
	cs.rs.LastCommit = cs.rs.Votes.Precommits(0)

	cs.updateToState(next)

	require.Equal(t, upgradedAppVersion, rec.appVersion.Load())
}
