package types

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMaxBlockMsgsForAppVersion(t *testing.T) {
	_, _, _, ok := MaxBlockMsgsForAppVersion(BlockTxLimitAppVersion - 1)
	require.False(t, ok)
	sdk, pfb, pff, ok := MaxBlockMsgsForAppVersion(BlockTxLimitAppVersion)
	require.True(t, ok)
	require.Equal(t, []int{600, 200, 200}, []int{sdk, pfb, pff})
	require.Equal(t, MaxBlockTxs, sdk+pfb+pff)
}
