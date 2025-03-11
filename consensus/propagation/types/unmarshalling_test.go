package types

import (
	"fmt"
	"testing"
	"time"

	"github.com/gogo/protobuf/proto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	cmtrand "github.com/tendermint/tendermint/libs/rand"
	"github.com/tendermint/tendermint/proto/tendermint/mempool"
	"github.com/tendermint/tendermint/state"
	"github.com/tendermint/tendermint/types"
)

// TestTxsToParts extensive testing of the txs to parts method
// that recovers the parts from mempool txs.
func TestTxsToParts(t *testing.T) {
	cleanup, _, sm := state.SetupTestCase(t)
	t.Cleanup(func() {
		cleanup(t)
	})
	numberOfTxs := 18 // increasing the number of transactions increases the test time exponentially
	txs := make([]types.Tx, 0, numberOfTxs)
	for i := 0; i < numberOfTxs; i++ {
		txs = append(txs, cmtrand.Bytes(int(types.BlockPartSizeBytes/3)))
	}

	data := types.Data{Txs: txs}
	block, partSet := sm.MakeBlock(1, data, types.RandCommit(time.Now()), []types.Evidence{}, cmtrand.Bytes(20))

	txsFound := make([]UnmarshalledTx, len(partSet.TxPos))
	for i, pos := range partSet.TxPos {
		// calculate the protobuf overhead
		protoTxs := mempool.Txs{Txs: [][]byte{data.Txs[i]}}
		marshalledTx, err := proto.Marshal(&protoTxs)
		require.NoError(t, err)

		txKey, err := types.TxKeyFromBytes(block.Txs[i].Hash())
		require.NoError(t, err)
		txsFound[i] = UnmarshalledTx{
			MetaData: TxMetaData{
				Start: uint32(pos.Start),
				End:   uint32(pos.End),
				Hash:  block.Txs[i].Hash(),
			},
			Key:     txKey,
			TxBytes: marshalledTx,
		}
	}

	// generate all the possible combinations for the provided number of transactions
	txsCombinations := GenerateTxsCombinations(numberOfTxs)

	for _, combination := range txsCombinations {
		t.Run(fmt.Sprintf("%v", combination), func(t *testing.T) {
			combinationTxs := make([]UnmarshalledTx, 0)
			for index, val := range combination {
				if val == 1 {
					combinationTxs = append(combinationTxs, txsFound[index])
				}
			}

			parts := TxsToParts(combinationTxs)

			for _, part := range parts {
				expectedPart := partSet.GetPart(int(part.Index))
				assert.Equal(t, expectedPart.Bytes, part.Bytes)
			}
		})
	}
}

// GenerateTxsCombinations generates all relevant transaction placements
// in a block given a number of transactions.
func GenerateTxsCombinations(n int) [][]int {
	total := 1 << n // 2^n combinations
	result := make([][]int, 0)

	for i := 0; i < total; i++ {
		bitArray := make([]int, n)
		for j := 0; j < n; j++ {
			// Extract the bit at position j
			bitArray[j] = (i >> j) & 1
		}
		result = append(result, bitArray)
	}
	return result
}

// checkMaxConsecutiveZeros ensures no more than 2 consecutive zeros exist
func checkMaxConsecutiveZeros(bits []int) bool {
	count := 0
	for _, bit := range bits {
		if bit == 0 {
			count++
			if count > 2 {
				return false
			}
		} else {
			count = 0
		}
	}
	return true
}
