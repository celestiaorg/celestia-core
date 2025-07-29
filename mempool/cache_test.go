package mempool

import (
	"crypto/rand"
	"crypto/sha256"
	"testing"

	"fmt"

	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/abci/example/kvstore"
	abci "github.com/cometbft/cometbft/abci/types"
	"github.com/cometbft/cometbft/proxy"
	"github.com/cometbft/cometbft/types"
)

func TestCacheAfterUpdate(t *testing.T) {
	app := kvstore.NewInMemoryApplication()
	cc := proxy.NewLocalClientCreator(app)
	mp, cleanup := newMempoolWithApp(cc)
	defer cleanup()

	// reAddIndices & txsInCache can have elements > numTxsToCreate
	// also assumes max index is 255 for convenience
	// txs in cache also checks order of elements
	tests := []struct {
		numTxsToCreate int
		updateIndices  []int
		reAddIndices   []int
		txsInCache     []int
	}{
		{1, []int{}, []int{1}, []int{1, 0}},    // adding new txs works
		{2, []int{1}, []int{}, []int{1, 0}},    // update doesn't remove tx from cache
		{2, []int{2}, []int{}, []int{2, 1, 0}}, // update adds new tx to cache
		{2, []int{1}, []int{1}, []int{1, 0}},   // re-adding after update doesn't make dupe
	}
	for tcIndex, tc := range tests {
		for i := 0; i < tc.numTxsToCreate; i++ {
			tx := kvstore.NewTx(fmt.Sprintf("%d", i), "value")
			err := mp.CheckTx(tx, func(resp *abci.ResponseCheckTx) {
				require.False(t, resp.IsErr())
			}, TxInfo{})
			require.NoError(t, err)
		}

		updateTxs := []*types.CachedTx{}
		for _, v := range tc.updateIndices {
			tx := kvstore.NewTx(fmt.Sprintf("%d", v), "value")
			updateTxs = append(updateTxs, types.Tx(tx).ToCachedTx())
		}
		err := mp.Update(int64(tcIndex), updateTxs, abciResponses(len(updateTxs), abci.CodeTypeOK), nil, nil)
		require.NoError(t, err)

		for _, v := range tc.reAddIndices {
			tx := kvstore.NewTx(fmt.Sprintf("%d", v), "value")
			_ = mp.CheckTx(tx, func(resp *abci.ResponseCheckTx) {
				require.False(t, resp.IsErr())
			}, TxInfo{})
		}

		cache := mp.cache.(*LRUTxCache)
		node := cache.GetList().Front()
		counter := 0
		for node != nil {
			require.NotEqual(t, len(tc.txsInCache), counter,
				"cache larger than expected on testcase %d", tcIndex)

			nodeVal := node.Value.(types.TxKey)
			expTx := kvstore.NewTx(fmt.Sprintf("%d", tc.txsInCache[len(tc.txsInCache)-counter-1]), "value")
			expectedBz := sha256.Sum256(expTx)
			// Reference for reading the errors:
			// >>> sha256('\x00').hexdigest()
			// '6e340b9cffb37a989ca544e6bb780a2c78901d3fb33738768511a30617afa01d'
			// >>> sha256('\x01').hexdigest()
			// '4bf5122f344554c53bde2ebb8cd2b7e3d1600ad631c385a5d7cce23c7785459a'
			// >>> sha256('\x02').hexdigest()
			// 'dbc1b4c900ffe48d575b5da5c638040125f65db0fe3e24494b76ea986457d986'

			require.EqualValues(t, expectedBz, nodeVal, "Equality failed on index %d, tc %d", counter, tcIndex)
			counter++
			node = node.Next()
		}
		require.Equal(t, len(tc.txsInCache), counter,
			"cache smaller than expected on testcase %d", tcIndex)
		mp.Flush()
	}
}

func TestCacheRemove(t *testing.T) {
	cache := NewLRUTxCache(100)
	numTxs := 10

	txs, err := populate(cache, numTxs)
	require.NoError(t, err)
	require.Equal(t, numTxs, len(cache.cacheMap))
	require.Equal(t, numTxs, cache.list.Len())

	for i := 0; i < numTxs; i++ {
		cache.Remove(&types.CachedTx{Tx: txs[i]})
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
		cache.Push(types.Tx(txBytes).ToCachedTx())
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
		cache.RemoveTxByKey(types.Tx(txs[i]).Key())
		// make sure its removed from both the map and the linked list
		require.Equal(t, numTxs-(i+1), len(cache.cacheMap))
		require.Equal(t, numTxs-(i+1), cache.list.Len())
	}
}

func TestLRUTxCacheWithCodes(t *testing.T) {
	cache := NewRejectedTxCache(10)
	tx := types.Tx("test-transaction").ToCachedTx()
	txKey := tx.Key()

	t.Run("initial state", func(t *testing.T) {
		require.False(t, cache.HasKey(txKey))
		code, ok := cache.Get(txKey)
		require.False(t, ok)
		require.Equal(t, uint32(0), code)
	})

	t.Run("add transaction with rejection code", func(t *testing.T) {
		initialCode := uint32(1001)
		wasNew := cache.Push(tx, initialCode)
		require.True(t, wasNew)

		require.True(t, cache.HasKey(txKey))
		code, ok := cache.Get(txKey)
		require.True(t, ok)
		require.Equal(t, initialCode, code)
	})

	t.Run("add same transaction again should overwrite", func(t *testing.T) {
		wasNew := cache.Push(tx, 1001)
		require.False(t, wasNew)
	})

	t.Run("updating code should not change existing entry", func(t *testing.T) {
		newCode := uint32(2002)
		wasNew := cache.Push(tx, newCode)
		require.False(t, wasNew)

		retrievedCode, ok := cache.Get(txKey)
		require.True(t, ok)
		require.Equal(t, uint32(1001), retrievedCode) // Should still be original code
	})

	t.Run("remove transaction", func(t *testing.T) {
		cache.Remove(tx)
		require.False(t, cache.HasKey(txKey))
		_, ok := cache.Get(txKey)
		require.False(t, ok)
	})

	t.Run("reset cache", func(t *testing.T) {
		cache.Reset()
		require.False(t, cache.HasKey(txKey))
	})
}
