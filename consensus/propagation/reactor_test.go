package propagation

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tendermint/tendermint/libs/bits"
)

func TestChunkParts(t *testing.T) {
	tests := []struct {
		name       string
		bitArray   *bits.BitArray
		peerCount  int
		redundancy int
		expected   []*bits.BitArray
	}{
		{
			name:       "Basic case with redundancy",
			bitArray:   bits.NewBitArray(6),
			peerCount:  3,
			redundancy: 2,
			expected: []*bits.BitArray{
				createBitArray(6, []int{0, 1, 2, 3}),
				createBitArray(6, []int{0, 1, 4, 5}),
				createBitArray(6, []int{2, 3, 4, 5}),
			},
		},
		{
			name:       "No redundancy",
			bitArray:   bits.NewBitArray(6),
			peerCount:  3,
			redundancy: 1,
			expected: []*bits.BitArray{
				createBitArray(6, []int{0, 1}),
				createBitArray(6, []int{2, 3}),
				createBitArray(6, []int{4, 5}),
			},
		},
		{
			name:       "Full overlap",
			bitArray:   bits.NewBitArray(4),
			peerCount:  2,
			redundancy: 2,
			expected: []*bits.BitArray{
				createBitArray(4, []int{0, 1, 2, 3}),
				createBitArray(4, []int{0, 1, 2, 3}),
			},
		},
		{
			name:       "uneven",
			bitArray:   bits.NewBitArray(4),
			peerCount:  3,
			redundancy: 2,
			expected: []*bits.BitArray{
				createBitArray(4, []int{0, 1, 2, 3}),
				createBitArray(4, []int{0, 1, 2, 3}),
				createBitArray(4, []int{0, 1, 2, 3}),
			},
		},
		{
			name:       "uneven",
			bitArray:   bits.NewBitArray(4),
			peerCount:  9,
			redundancy: 1,
			expected: []*bits.BitArray{
				createBitArray(4, []int{0, 1, 2, 3}),
				createBitArray(4, []int{0, 1, 2, 3}),
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ba := tc.bitArray
			ba.Fill()
			result := chunkParts(ba, tc.peerCount, tc.redundancy)
			require.Equal(t, len(tc.expected), len(result))

			for i, exp := range tc.expected {
				require.Equal(t, exp.String(), result[i].String(), i)
			}
		})
	}
}

func createBitArray(size int, indices []int) *bits.BitArray {
	ba := bits.NewBitArray(size)
	for _, index := range indices {
		ba.SetIndex(index, true)
	}
	return ba
}
