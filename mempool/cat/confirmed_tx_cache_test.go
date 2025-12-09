package cat

import (
	"testing"

	"github.com/cometbft/cometbft/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfirmedTxCache_AddAndGet(t *testing.T) {
	cache := newConfirmedTxCache(1024 * 1024) // 1MB

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
	cache := newConfirmedTxCache(1024 * 1024) // 1MB

	tx := types.Tx("test transaction")
	txKey := tx.Key()

	// Should not find tx that wasn't added
	_, ok := cache.Get(txKey)
	assert.False(t, ok)
}

func TestConfirmedTxCache_MemoryLimit(t *testing.T) {
	cache := newConfirmedTxCache(100) // Small limit for testing

	// Add txs that exceed the limit
	tx1 := types.Tx("transaction 1 - 30 bytes!!!!")
	tx2 := types.Tx("transaction 2 - 30 bytes!!!!")
	tx3 := types.Tx("transaction 3 - 30 bytes!!!!")
	tx4 := types.Tx("transaction 4 - 30 bytes!!!!")

	cache.Add(tx1.Key(), tx1, 100)
	cache.Add(tx2.Key(), tx2, 101)
	cache.Add(tx3.Key(), tx3, 102)

	assert.Equal(t, 3, cache.Size())
	assert.LessOrEqual(t, cache.Bytes(), int64(100))

	// Adding tx4 should evict tx1 (lowest height)
	cache.Add(tx4.Key(), tx4, 103)

	assert.Equal(t, 3, cache.Size())

	// tx1 should be evicted (lowest height)
	_, ok := cache.Get(tx1.Key())
	assert.False(t, ok, "tx1 should be evicted")

	// tx2, tx3, tx4 should still exist
	_, ok = cache.Get(tx2.Key())
	assert.True(t, ok, "tx2 should exist")

	_, ok = cache.Get(tx3.Key())
	assert.True(t, ok, "tx3 should exist")

	_, ok = cache.Get(tx4.Key())
	assert.True(t, ok, "tx4 should exist")
}

func TestConfirmedTxCache_EvictsLowestHeightFirst(t *testing.T) {
	cache := newConfirmedTxCache(100)

	// Add txs in non-sequential height order
	tx1 := types.Tx("tx at height 105!!!!!!!!!!!")
	tx2 := types.Tx("tx at height 100!!!!!!!!!!!")
	tx3 := types.Tx("tx at height 110!!!!!!!!!!!")

	cache.Add(tx1.Key(), tx1, 105)
	cache.Add(tx2.Key(), tx2, 100) // Lowest height
	cache.Add(tx3.Key(), tx3, 110)

	assert.Equal(t, 3, cache.Size())

	// Add another tx to trigger eviction
	tx4 := types.Tx("tx at height 115!!!!!!!!!!!")
	cache.Add(tx4.Key(), tx4, 115)

	// tx2 should be evicted (lowest height 100)
	_, ok := cache.Get(tx2.Key())
	assert.False(t, ok, "tx2 (height 100) should be evicted first")

	// Others should exist
	_, ok = cache.Get(tx1.Key())
	assert.True(t, ok, "tx1 (height 105) should exist")

	_, ok = cache.Get(tx3.Key())
	assert.True(t, ok, "tx3 (height 110) should exist")

	_, ok = cache.Get(tx4.Key())
	assert.True(t, ok, "tx4 (height 115) should exist")
}

func TestConfirmedTxCache_NoDuplicates(t *testing.T) {
	cache := newConfirmedTxCache(1024 * 1024)

	tx := types.Tx("test transaction")
	txKey := tx.Key()

	cache.Add(txKey, tx, 100)
	initialBytes := cache.Bytes()

	// Adding same tx again should not increase size
	cache.Add(txKey, tx, 101)

	assert.Equal(t, 1, cache.Size())
	assert.Equal(t, initialBytes, cache.Bytes())
}

func TestConfirmedTxCache_LargeTxRejected(t *testing.T) {
	cache := newConfirmedTxCache(50)

	// Try to add a tx larger than maxBytes
	largeTx := types.Tx("this transaction is way too large to fit in the cache!!!!!!")

	cache.Add(largeTx.Key(), largeTx, 100)

	// Should not be added
	assert.Equal(t, 0, cache.Size())
	_, ok := cache.Get(largeTx.Key())
	assert.False(t, ok)
}

func TestConfirmedTxCache_BytesTracking(t *testing.T) {
	cache := newConfirmedTxCache(1024 * 1024)

	tx1 := types.Tx("short")
	tx2 := types.Tx("medium length tx")
	tx3 := types.Tx("this is a longer transaction")

	cache.Add(tx1.Key(), tx1, 100)
	assert.Equal(t, int64(len(tx1)), cache.Bytes())

	cache.Add(tx2.Key(), tx2, 101)
	assert.Equal(t, int64(len(tx1)+len(tx2)), cache.Bytes())

	cache.Add(tx3.Key(), tx3, 102)
	assert.Equal(t, int64(len(tx1)+len(tx2)+len(tx3)), cache.Bytes())
}

func TestConfirmedTxCache_Prune(t *testing.T) {
	cache := newConfirmedTxCache(1024 * 1024)

	// Add txs at different heights
	tx1 := types.Tx("tx at height 100")
	tx2 := types.Tx("tx at height 105")
	tx3 := types.Tx("tx at height 110")
	tx4 := types.Tx("tx at height 115")
	tx5 := types.Tx("tx at height 120")

	cache.Add(tx1.Key(), tx1, 100)
	cache.Add(tx2.Key(), tx2, 105)
	cache.Add(tx3.Key(), tx3, 110)
	cache.Add(tx4.Key(), tx4, 115)
	cache.Add(tx5.Key(), tx5, 120)

	assert.Equal(t, 5, cache.Size())

	// Prune everything below height 110
	cache.Prune(110)

	assert.Equal(t, 3, cache.Size())

	// tx1 and tx2 should be gone
	_, ok := cache.Get(tx1.Key())
	assert.False(t, ok, "tx1 (height 100) should be pruned")

	_, ok = cache.Get(tx2.Key())
	assert.False(t, ok, "tx2 (height 105) should be pruned")

	// tx3, tx4, tx5 should still exist
	_, ok = cache.Get(tx3.Key())
	assert.True(t, ok, "tx3 (height 110) should exist")

	_, ok = cache.Get(tx4.Key())
	assert.True(t, ok, "tx4 (height 115) should exist")

	_, ok = cache.Get(tx5.Key())
	assert.True(t, ok, "tx5 (height 120) should exist")

	// Verify bytes are tracked correctly
	expectedBytes := int64(len(tx3) + len(tx4) + len(tx5))
	assert.Equal(t, expectedBytes, cache.Bytes())
}

func TestConfirmedTxCache_PruneAll(t *testing.T) {
	cache := newConfirmedTxCache(1024 * 1024)

	tx1 := types.Tx("tx at height 100")
	tx2 := types.Tx("tx at height 105")

	cache.Add(tx1.Key(), tx1, 100)
	cache.Add(tx2.Key(), tx2, 105)

	assert.Equal(t, 2, cache.Size())

	// Prune everything (min height higher than all entries)
	cache.Prune(200)

	assert.Equal(t, 0, cache.Size())
	assert.Equal(t, int64(0), cache.Bytes())
}

func TestConfirmedTxCache_PruneNone(t *testing.T) {
	cache := newConfirmedTxCache(1024 * 1024)

	tx1 := types.Tx("tx at height 100")
	tx2 := types.Tx("tx at height 105")

	cache.Add(tx1.Key(), tx1, 100)
	cache.Add(tx2.Key(), tx2, 105)

	initialSize := cache.Size()
	initialBytes := cache.Bytes()

	// Prune nothing (min height lower than all entries)
	cache.Prune(50)

	assert.Equal(t, initialSize, cache.Size())
	assert.Equal(t, initialBytes, cache.Bytes())
}
