package cat

import (
	"testing"

	"github.com/cometbft/cometbft/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfirmedTxCache_AddAndGet(t *testing.T) {
	cache := newConfirmedTxCache()

	tx := types.Tx("test transaction")
	txKey := tx.Key()

	// Add tx to cache at height 100
	cache.Add(txKey, tx, 100)

	// Should be able to retrieve it
	retrieved, ok := cache.Get(txKey)
	require.True(t, ok)
	assert.Equal(t, tx, types.Tx(retrieved))
}

func TestConfirmedTxCache_NotFound(t *testing.T) {
	cache := newConfirmedTxCache()

	tx := types.Tx("test transaction")
	txKey := tx.Key()

	// Should not find tx that wasn't added
	_, ok := cache.Get(txKey)
	assert.False(t, ok)
}

func TestConfirmedTxCache_PruneOlderThan(t *testing.T) {
	cache := newConfirmedTxCache()

	tx1 := types.Tx("test transaction 1")
	tx2 := types.Tx("test transaction 2")
	tx3 := types.Tx("test transaction 3")

	// Add txs at different heights
	cache.Add(tx1.Key(), tx1, 100)
	cache.Add(tx2.Key(), tx2, 105)
	cache.Add(tx3.Key(), tx3, 110)

	assert.Equal(t, 3, cache.Size())

	// Prune entries at or before height 105
	cache.PruneOlderThan(105)

	// Only tx3 should remain
	assert.Equal(t, 1, cache.Size())

	_, ok := cache.Get(tx1.Key())
	assert.False(t, ok, "tx1 should be pruned")

	_, ok = cache.Get(tx2.Key())
	assert.False(t, ok, "tx2 should be pruned")

	_, ok = cache.Get(tx3.Key())
	assert.True(t, ok, "tx3 should still exist")
}

func TestConfirmedTxCache_MaxSize(t *testing.T) {
	cache := newConfirmedTxCache()
	cache.maxSize = 3

	// Add 3 txs
	for i := 0; i < 3; i++ {
		tx := types.Tx([]byte{byte(i)})
		cache.Add(tx.Key(), tx, int64(100+i))
	}

	assert.Equal(t, 3, cache.Size())

	// Adding a 4th tx when at max size should not increase size
	tx4 := types.Tx([]byte{4})
	cache.Add(tx4.Key(), tx4, 103)

	// Size should not exceed max
	assert.Equal(t, 3, cache.Size())

	// tx4 should not be found (wasn't added due to max size)
	_, ok := cache.Get(tx4.Key())
	assert.False(t, ok)
}

func TestConfirmedTxCache_HeightTTL(t *testing.T) {
	cache := newConfirmedTxCache()

	// Add tx at height 100
	tx := types.Tx("test transaction")
	cache.Add(tx.Key(), tx, 100)

	// Prune entries at or before height 99 - tx at 100 should still exist
	cache.PruneOlderThan(99)
	_, ok := cache.Get(tx.Key())
	assert.True(t, ok, "tx at height 100 should exist when pruning <= 99")

	// Prune entries at or before height 100 - tx at 100 should be pruned
	cache.PruneOlderThan(100)
	_, ok = cache.Get(tx.Key())
	assert.False(t, ok, "tx at height 100 should be pruned when pruning <= 100")
}

func TestConfirmedTxCache_HeightBasedCleanup(t *testing.T) {
	// Simulate the cleanup logic used in heightSignalLoop
	cache := newConfirmedTxCache()
	heightTTL := int64(15)

	// Add txs at heights 100, 105, 110
	tx1 := types.Tx("tx at height 100")
	tx2 := types.Tx("tx at height 105")
	tx3 := types.Tx("tx at height 110")

	cache.Add(tx1.Key(), tx1, 100)
	cache.Add(tx2.Key(), tx2, 105)
	cache.Add(tx3.Key(), tx3, 110)

	// At current height 115, prune entries older than 115-15=100
	// This should remove tx1 (height 100 <= 100)
	currentHeight := int64(115)
	cache.PruneOlderThan(currentHeight - heightTTL)

	_, ok := cache.Get(tx1.Key())
	assert.False(t, ok, "tx1 at height 100 should be pruned at height 115")

	_, ok = cache.Get(tx2.Key())
	assert.True(t, ok, "tx2 at height 105 should exist at height 115")

	_, ok = cache.Get(tx3.Key())
	assert.True(t, ok, "tx3 at height 110 should exist at height 115")

	// At current height 120, prune entries older than 120-15=105
	currentHeight = 120
	cache.PruneOlderThan(currentHeight - heightTTL)

	_, ok = cache.Get(tx2.Key())
	assert.False(t, ok, "tx2 at height 105 should be pruned at height 120")

	_, ok = cache.Get(tx3.Key())
	assert.True(t, ok, "tx3 at height 110 should exist at height 120")
}
