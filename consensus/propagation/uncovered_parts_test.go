package propagation

import (
	"testing"

	"github.com/stretchr/testify/require"

	proptypes "github.com/cometbft/cometbft/consensus/propagation/types"
	"github.com/cometbft/cometbft/state"
)

func TestUncoveredParts(t *testing.T) {
	const partSize = 100

	tx := func(start, end uint32) proptypes.TxMetaData {
		return proptypes.TxMetaData{Start: start, End: end}
	}

	tests := map[string]struct {
		txs     []proptypes.TxMetaData
		total   uint32
		lastLen uint32
		want    []uint32
	}{
		"header only": {
			total:   1,
			lastLen: 40,
			want:    []uint32{0},
		},
		"empty block": {
			total:   2,
			lastLen: partSize,
			want:    []uint32{0, 1},
		},
		"header and last commit straddling a boundary": {
			txs:     []proptypes.TxMetaData{tx(10, 190)},
			total:   3,
			lastLen: 50,
			want:    []uint32{0, 1, 2},
		},
		"several transactions back to back": {
			txs:     []proptypes.TxMetaData{tx(50, 180), tx(180, 320)},
			total:   4,
			lastLen: 50,
			want:    []uint32{0, 3},
		},
		"gap between transactions pushes nothing": {
			txs:     []proptypes.TxMetaData{tx(0, 100), tx(250, 400)},
			total:   4,
			lastLen: partSize,
			want:    nil,
		},
		"out of order transactions push nothing": {
			txs:     []proptypes.TxMetaData{tx(180, 320), tx(50, 180)},
			total:   4,
			lastLen: 50,
			want:    nil,
		},
		"short last part covered": {
			txs:     []proptypes.TxMetaData{tx(0, 140)},
			total:   2,
			lastLen: 40,
			want:    []uint32{},
		},
		"exactly the cap": {
			total:   maxPushedParts,
			lastLen: partSize,
			want:    []uint32{0, 1, 2, 3, 4, 5},
		},
		"above the cap pushes nothing": {
			total:   maxPushedParts + 1,
			lastLen: partSize,
			want:    nil,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, uncoveredParts(tc.txs, tc.total, partSize, tc.lastLen))
		})
	}
}

// TestRealBlockTxsAreBackToBack checks the assumption uncoveredParts relies
// on: in a real encoded block, each tx starts where the previous one ends.
// If the block layout ever changes, this fails instead of pushing silently
// turning off.
func TestRealBlockTxsAreBackToBack(t *testing.T) {
	cleanup, _, sm, pv := state.SetupTestCaseWithPrivVal(t)
	t.Cleanup(func() { cleanup(t) })

	const txCount = 100
	_, _, _, txs := createTestProposal(t, sm, pv, 1, 0, txCount, 1000)

	require.Len(t, txs, txCount)
	require.True(t, txsAreBackToBack(txs))
}
