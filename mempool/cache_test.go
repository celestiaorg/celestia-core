package mempool

import (
	"crypto/rand"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/types"
)

func TestCacheRemove(t *testing.T) {
	cache := NewLRUTxCache(100)
	numTxs := 10

	txs, err := populate(cache, numTxs)
	require.NoError(t, err)
	require.Equal(t, numTxs, len(cache.cacheMap))
	require.Equal(t, numTxs, cache.list.Len())

	for i := 0; i < numTxs; i++ {
		cache.Remove(types.TxKey(txs[i]))
		// make sure its removed from both the map and the linked list
		require.Len(t, cache.cacheMap, numTxs-(i+1))
		require.Equal(t, numTxs-(i+1), cache.list.Len())
	}
}

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
		cache.Push(types.TxKey(txBytes))
	}
	return txs, nil
}

func TestCacheRemoveByKey(t *testing.T) {
	cache := NewLRUTxCache(100)
	numTxs := 10

	txs, err := populate(cache, numTxs)
	require.NoError(t, err)
	require.Equal(t, numTxs, len(cache.cacheMap))
	require.Equal(t, numTxs, cache.list.Len())

	for i := 0; i < numTxs; i++ {
		cache.Remove(types.TxKey(txs[i]))
		// make sure its removed from both the map and the linked list
		require.Equal(t, numTxs-(i+1), len(cache.cacheMap))
		require.Equal(t, numTxs-(i+1), cache.list.Len())
	}
}

func TestRejectedTxCache(t *testing.T) {
	cacheSize := 10
	cache := NewRejectedTxCache(cacheSize)
	tx := types.Tx("test-transaction")
	txKey := tx.Key()
	initialCode := uint32(1001)
	initialLog := "initial-log"

	t.Run("initial state", func(t *testing.T) {
		require.False(t, cache.HasKey(txKey))
		code, log, ok := cache.Get(txKey)
		require.False(t, ok)
		require.Equal(t, uint32(0), code)
		require.Equal(t, "", log)
	})

	t.Run("add transaction with rejection code and log", func(t *testing.T) {
		wasNew := cache.Push(txKey, initialCode, initialLog)
		require.True(t, wasNew)

		require.True(t, cache.HasKey(txKey))
		code, log, ok := cache.Get(txKey)
		require.True(t, ok)
		require.Equal(t, initialCode, code)
		require.Equal(t, initialLog, log)
	})

	t.Run("add same transaction again should not overwrite", func(t *testing.T) {
		wasNew := cache.Push(txKey, initialCode, initialLog)
		require.False(t, wasNew)
	})

	t.Run("should not change existing entry", func(t *testing.T) {
		newCode := uint32(2002)
		newLog := "new-log"
		wasNew := cache.Push(txKey, newCode, newLog)
		require.False(t, wasNew)

		retrievedCode, retrievedLog, ok := cache.Get(txKey)
		require.True(t, ok)
		require.Equal(t, initialCode, retrievedCode) // Should still be original code
		require.Equal(t, initialLog, retrievedLog)
	})

	t.Run("remove transaction", func(t *testing.T) {
		cache.Remove(txKey)
		require.False(t, cache.HasKey(txKey))
		_, _, ok := cache.Get(txKey)
		require.False(t, ok)
	})

	t.Run("make sure that the cache size is respected", func(t *testing.T) {
		// cache size is 10, adding 15 txs
		// cache should remain within the size limit
		for i := 0; i < 15; i++ {
			cache.Push(txKey, initialCode, initialLog)
		}
		require.Equal(t, cacheSize, cache.cache.size)
	})

	t.Run("reset cache", func(t *testing.T) {
		cache.Reset()
		require.False(t, cache.HasKey(txKey))
	})
}
