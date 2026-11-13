package consensus

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/consensus/propagation"
)

// blockMaxBytesRecorder records the Block.MaxBytes values consensus installs
// on the propagator.
type blockMaxBytesRecorder struct {
	*propagation.NoOpPropagator
	mtx      sync.Mutex
	maxBytes []int64
}

func (r *blockMaxBytesRecorder) SetBlockMaxBytes(maxBytes int64) {
	r.mtx.Lock()
	defer r.mtx.Unlock()
	r.maxBytes = append(r.maxBytes, maxBytes)
}

func (r *blockMaxBytesRecorder) last() (int64, bool) {
	r.mtx.Lock()
	defer r.mtx.Unlock()
	if len(r.maxBytes) == 0 {
		return 0, false
	}
	return r.maxBytes[len(r.maxBytes)-1], true
}

// TestUpdateToStateRefreshesPropagatorBlockMaxBytes ensures that a
// Block.MaxBytes change in state reaches the propagation reactor when
// consensus moves to the next height, without a restart.
func TestUpdateToStateRefreshesPropagatorBlockMaxBytes(t *testing.T) {
	cs, _ := randState(1)
	rec := &blockMaxBytesRecorder{NoOpPropagator: propagation.NewNoOpPropagator()}
	cs.propagator = rec

	next := cs.state.Copy()
	next.LastBlockHeight = cs.state.LastBlockHeight + 1
	next.ConsensusParams.Block.MaxBytes = cs.state.ConsensusParams.Block.MaxBytes * 2
	// updateToState requires a last commit after the initial height.
	cs.rs.LastCommit = cs.rs.Votes.Precommits(0)

	cs.updateToState(next)

	got, ok := rec.last()
	require.True(t, ok, "propagator did not receive Block.MaxBytes")
	require.Equal(t, next.ConsensusParams.Block.MaxBytes, got)
}
