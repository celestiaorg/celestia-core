package cat

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/types"
)

func TestTxSetZeroGasDoesNotPanic(t *testing.T) {
	wtx := &wrappedTx{
		tx:        &types.CachedTx{Tx: types.Tx("zero-gas tx")},
		gasWanted: 0,
		priority:  10,
	}
	var set *txSet
	require.NotPanics(t, func() { set = newTxSet(wtx) })
	require.EqualValues(t, 0, set.aggregatedPriority)
}
