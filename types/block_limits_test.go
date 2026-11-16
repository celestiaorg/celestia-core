package types

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMaxBlockMsgsForAppVersion(t *testing.T) {
	for _, v := range []uint64{BlockTxLimitAppVersion - 1, BlockTxLimitAppVersion + 1} {
		_, _, _, ok := MaxBlockMsgsForAppVersion(v)
		require.False(t, ok, v) // caps core does not know: no early release
	}
	sdk, pfb, pff, ok := MaxBlockMsgsForAppVersion(BlockTxLimitAppVersion)
	require.True(t, ok)
	require.Equal(t, []int{600, 200, 200}, []int{sdk, pfb, pff})
	require.Equal(t, MaxBlockTxs, sdk+pfb+pff)
}
