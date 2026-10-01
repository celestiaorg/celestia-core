package parallel

import (
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestForVisitsEveryIndexExactlyOnce(t *testing.T) {
	for _, n := range []int{0, 1, 2, 7, 64, 1000} {
		for _, grain := range []int{0, 1, 4, 512} {
			seen := make([]int32, n)
			For(n, grain, func(i int) { atomic.AddInt32(&seen[i], 1) })
			for i, count := range seen {
				require.Equal(t, int32(1), count, "n=%d grain=%d index=%d", n, grain, i)
			}
		}
	}
}

func TestForHandlesNegativeCount(t *testing.T) {
	var calls int32
	For(-5, 1, func(int) { atomic.AddInt32(&calls, 1) })
	require.Zero(t, calls)
}
