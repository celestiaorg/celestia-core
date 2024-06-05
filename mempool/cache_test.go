package mempool

import (
	"crypto/rand"
	"testing"

	"github.com/cometbft/cometbft/types"
	"github.com/stretchr/testify/require"
)

func populate(cache TxCache, numTxs int) ([][]byte, error) {

	txs := make([][]byte, numTxs)
	for i := 0; i < numTxs; i++ {
		// probability of collision is 2**-256
		txBytes := make([]byte, 32)
		_, err := rand.Read(txBytes)

		if err != nil {
			return nil, err
		}

		txs[i] = txBytes
		cache.Push(types.Tx(txBytes).Key())
	}

	return txs, nil
}

func TestCacheRemove(t *testing.T) {
	cache := NewLRUTxCache(100)
	numTxs := 10

	txs, err := populate(cache, numTxs)
	require.NoError(t, err)
	require.Equal(t, numTxs, len(cache.cacheMap))
	require.Equal(t, numTxs, cache.list.Len())

	for i := 0; i < numTxs; i++ {
		cache.Remove(types.Tx(txs[i]).Key())
		// make sure its removed from both the map and the linked list
		require.Equal(t, numTxs-(i+1), len(cache.cacheMap))
		require.Equal(t, numTxs-(i+1), cache.list.Len())
	}
}

func TestCacheRemoveByKey(t *testing.T) {
	cache := NewLRUTxCache(100)
	numTxs := 10

	txs, err := populate(cache, numTxs)
	require.NoError(t, err)
	require.Equal(t, numTxs, len(cache.cacheMap))
	require.Equal(t, numTxs, cache.list.Len())

	for i := 0; i < numTxs; i++ {
		cache.RemoveTxByKey(types.Tx(txs[i]).Key())
		// make sure its removed from both the map and the linked list
		require.Equal(t, numTxs-(i+1), len(cache.cacheMap))
		require.Equal(t, numTxs-(i+1), cache.list.Len())
	}
}
