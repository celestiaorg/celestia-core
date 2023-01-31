package cat_test

import (
	"fmt"
	"math/rand"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tendermint/tendermint/mempool/cat"
)

func TestBitArray(t *testing.T) {
	sizes := []int{7, 8, 9, 63, 64, 65}

	for _, size := range sizes {
		t.Run(fmt.Sprintf("size%d", size), func(t *testing.T) {
			bitArray := cat.NewBitArray(size)
			setValues := make(map[int]struct{})
			for i := 0; i < size; i++ {
				index := rand.Intn(int(size))
				bitArray.Set(index)
				setValues[index] = struct{}{}
			}
			for i := 0; i < size; i++ {
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

func TestBitArrayWrongSize(t *testing.T) {
	_, err := cat.NewBitArrayFromBytes(9, []byte{0x01})
	require.Error(t, err)

	_, err = cat.NewBitArrayFromBytes(8, []byte{0x01, 0x01})
	require.Error(t, err)

	_, err = cat.NewBitArrayFromBytes(17, []byte{0x01, 0x01})
	require.Error(t, err)
}
