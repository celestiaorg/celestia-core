package cat

import (
	"bytes"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/types"
)

func TestStoreSimple(t *testing.T) {
	store := newStore()

	tx := types.Tx("tx1")
	key := tx.Key()
	wtx := newWrappedTx(tx.ToCachedTx(), 1, 1, 1, "")

	// asset zero state
	require.Nil(t, store.get(key))
	require.False(t, store.has(key))
	require.False(t, store.remove(key))
	require.Zero(t, store.size())
	require.Zero(t, store.totalBytes())
	require.Empty(t, store.getAllKeys())
	require.Empty(t, store.getAllTxs())

	// add a tx
	store.set(wtx)
	require.True(t, store.has(key))
	require.Equal(t, wtx, store.get(key))
	require.Equal(t, int(1), store.size())
	require.Equal(t, wtx.size(), store.totalBytes())

	// remove a tx
	store.remove(key)
	require.False(t, store.has(key))
	require.Nil(t, store.get(key))
	require.Zero(t, store.size())
	require.Zero(t, store.totalBytes())
	require.Empty(t, store.orderedTxs)
	require.Empty(t, store.txs)
}

func TestStoreOrdering(t *testing.T) {
	store := newStore()

	tx1 := types.Tx("tx1")
	tx2 := types.Tx("tx2")
	tx3 := types.Tx("tx3")

	// Create wrapped txs with different priorities
	wtx1 := newWrappedTx(tx1.ToCachedTx(), 1, 1, 1, "")
	wtx2 := newWrappedTx(tx2.ToCachedTx(), 2, 2, 2, "")
	wtx3 := newWrappedTx(tx3.ToCachedTx(), 3, 3, 3, "")

	// Add txs in reverse priority order
	store.set(wtx1)
	store.set(wtx2)
	store.set(wtx3)

	// Check that iteration returns txs in correct priority order
	var orderedTxs []*wrappedTx
	store.iterateOrderedTxs(func(tx *wrappedTx) bool {
		orderedTxs = append(orderedTxs, tx)
		return true
	})

	require.Equal(t, 3, len(orderedTxs))
	require.Equal(t, wtx3, orderedTxs[0])
	require.Equal(t, wtx2, orderedTxs[1])
	require.Equal(t, wtx1, orderedTxs[2])
}

func TestStore(t *testing.T) {
	t.Run("deleteOrderedTx", func(*testing.T) {
		store := newStore()

		tx1 := types.Tx("tx1")
		tx2 := types.Tx("tx2")
		tx3 := types.Tx("tx3")

		// Create wrapped txs with different priorities
		wtx1 := newWrappedTx(tx1.ToCachedTx(), 1, 1, 1, "")
		wtx2 := newWrappedTx(tx2.ToCachedTx(), 2, 2, 2, "")
		wtx3 := newWrappedTx(tx3.ToCachedTx(), 3, 3, 3, "")

		// Add txs in reverse priority order
		store.set(wtx1)
		store.set(wtx2)
		store.set(wtx3)

		orderedTxs := getOrderedTxs(store)
		require.Equal(t, []*wrappedTx{wtx3, wtx2, wtx1}, orderedTxs)

		err := store.deleteOrderedTx(wtx2)
		require.NoError(t, err)
		require.Equal(t, []*wrappedTx{wtx3, wtx1}, getOrderedTxs(store))

		err = store.deleteOrderedTx(wtx3)
		require.NoError(t, err)
		require.Equal(t, []*wrappedTx{wtx1}, getOrderedTxs(store))

		err = store.deleteOrderedTx(wtx1)
		require.NoError(t, err)
		require.Equal(t, []*wrappedTx{}, getOrderedTxs(store))

		err = store.deleteOrderedTx(wtx1)
		require.ErrorContains(t, err, "ordered transactions list is empty")
	})
}

func getOrderedTxs(store *store) []*wrappedTx {
	orderedTxs := []*wrappedTx{}
	store.iterateOrderedTxs(func(tx *wrappedTx) bool {
		orderedTxs = append(orderedTxs, tx)
		return true
	})
	return orderedTxs
}

func TestStoreReservingTxs(t *testing.T) {
	store := newStore()

	tx := types.Tx("tx1")
	key := tx.Key()
	wtx := newWrappedTx(tx.ToCachedTx(), 1, 1, 1, "")

	// asset zero state
	store.release(key)

	// reserve a tx
	store.reserve(key)
	require.True(t, store.isReserved(key))
	// should not update the total bytes
	require.Zero(t, store.totalBytes())

	// should be able to add a tx
	store.set(wtx)
	require.Equal(t, tx, store.get(key).tx.Tx)
	require.Equal(t, wtx.size(), store.totalBytes())

	// releasing should do nothing on a set tx
	store.release(key)
	require.True(t, store.has(key))
	require.Equal(t, tx, store.get(key).tx.Tx)

	store.remove(key)
	require.False(t, store.has(key))

	// reserve the tx again
	store.reserve(key)
	require.True(t, store.isReserved(key))

	// release should remove the tx
	store.release(key)
	require.False(t, store.has(key))
}

func TestReadReserved(t *testing.T) {
	store := newStore()
	tx := types.Tx("tx1")
	store.reserve(tx.Key())

	require.Nil(t, store.get(tx.Key()))
	require.False(t, store.has(tx.Key()))
	require.Len(t, store.getAllKeys(), 0)
	require.Len(t, store.getAllTxs(), 0)
}

func TestStoreConcurrentAccess(t *testing.T) {
	store := newStore()

	numTxs := 100

	wg := &sync.WaitGroup{}
	for i := 0; i < numTxs; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ticker := time.NewTicker(10 * time.Millisecond)
			for range ticker.C {
				tx := types.Tx(fmt.Sprintf("tx%d", i%(numTxs/10)))
				key := tx.Key()
				wtx := newWrappedTx(tx.ToCachedTx(), 1, 1, 1, "")
				existingTx := store.get(key)
				if existingTx != nil && bytes.Equal(existingTx.tx.Tx, tx) {
					// tx has already been added
					return
				}
				if store.reserve(key) {
					// some fail
					if i%3 == 0 {
						store.release(key)
						return
					}
					store.set(wtx)
					// this should be a noop
					store.release(key)
					return
				}
				// already reserved so we retry in 10 milliseconds
			}
		}(i)
	}
	wg.Wait()

	require.Equal(t, numTxs/10, store.size())
}

func TestStoreGetTxs(t *testing.T) {
	store := newStore()

	numTxs := 100
	for i := 0; i < numTxs; i++ {
		tx := types.Tx(fmt.Sprintf("tx%d", i))
		wtx := newWrappedTx(tx.ToCachedTx(), 1, 1, int64(i), "")
		store.set(wtx)
	}

	require.Equal(t, numTxs, store.size())

	// get all txs
	txs := store.getAllTxs()
	require.Equal(t, numTxs, len(txs))

	// get txs by keys
	keys := store.getAllKeys()
	require.Equal(t, numTxs, len(keys))

	// get txs below a certain priority
	txs, bz := store.getTxsBelowPriority(int64(numTxs / 2))
	require.Equal(t, numTxs/2, len(txs))
	var actualBz int64
	for _, tx := range txs {
		actualBz += tx.size()
	}
	require.Equal(t, actualBz, bz)
}

func TestStoreExpiredTxs(t *testing.T) {
	store := newStore()
	numTxs := 100
	for i := 0; i < numTxs; i++ {
		tx := types.Tx(fmt.Sprintf("tx%d", i))
		wtx := newWrappedTx(tx.ToCachedTx(), int64(i), 1, 1, "")
		store.set(wtx)
	}

	// half of them should get purged
	store.purgeExpiredTxs(int64(numTxs/2), time.Time{})

	remainingTxs := store.getAllTxs()
	require.Equal(t, numTxs/2, len(remainingTxs))
	for _, tx := range remainingTxs {
		require.GreaterOrEqual(t, tx.height, int64(numTxs/2))
	}

	store.purgeExpiredTxs(int64(0), time.Now().Add(time.Second))
	require.Empty(t, store.getAllTxs())
}
