package cat_test

import (
	"fmt"
	"math/rand"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tendermint/tendermint/mempool/cat"
)

func TestBitArray(t *testing.T) {
	sizes := []uint64{7, 8, 9, 63, 64, 65}

	for _, size := range sizes {
		t.Run(fmt.Sprintf("size%d", size), func(t *testing.T) {
			bitArray := cat.NewBitArray(size)
			setValues := make(map[uint64]struct{})
			for i := 0; i < int(size); i++ {
				index := uint64(rand.Intn(int(size)))
				bitArray.Set(index)
				setValues[index] = struct{}{}
			}
			for i := uint64(0); i < size; i++ {
				if _, ok := setValues[i]; ok {
					require.True(t, bitArray.Get(i))
				} else {
					require.False(t, bitArray.Get(i))
				}
			}
			bytes := bitArray.Bytes()
			newArray, err := cat.NewBitArrayFromBytes(size, bytes)
			require.NoError(t, err)
			require.Equal(t, newArray, bitArray)
		})
	}
}