package cat

import (
	"bytes"
	"fmt"
	"math/rand"
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
	wtx := newWrappedTx(tx.ToCachedTx(), 1, 1, 1, nil, 0)

	// asset zero state
	require.Nil(t, store.get(key))
	require.False(t, store.has(key))
	require.False(t, store.remove(key))
	require.Zero(t, store.size())
	require.Zero(t, store.totalBytes())
	require.Empty(t, store.getAllKeys())
	require.Empty(t, store.getOrderedTxs())

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
	require.Empty(t, store.getOrderedTxs())
	require.Empty(t, store.txs)
}

func TestStoreOrdering(t *testing.T) {
	store := newStore()

	tx1 := types.Tx("tx1")
	tx2 := types.Tx("tx2")
	tx3 := types.Tx("tx3")

	// Create wrapped txs with different priorities
	wtx1 := newWrappedTx(tx1.ToCachedTx(), 1, 1, 1, nil, 0)
	wtx2 := newWrappedTx(tx2.ToCachedTx(), 2, 2, 2, nil, 0)
	wtx3 := newWrappedTx(tx3.ToCachedTx(), 3, 3, 3, nil, 0)

	// Add txs in reverse priority order
	store.set(wtx1)
	store.set(wtx2)
	store.set(wtx3)

	// Check that iteration returns txs in correct priority order
	orderedTxs := store.getOrderedTxs()

	require.Equal(t, 3, len(orderedTxs))
	require.Equal(t, wtx3, orderedTxs[0])
	require.Equal(t, wtx2, orderedTxs[1])
	require.Equal(t, wtx1, orderedTxs[2])
}

func makeTxs(n int, sender []byte, withSequence bool) []*wrappedTx {
	sequence := uint64(0)
	txs := make([]*wrappedTx, 0)
	for i := 1; i <= n; i++ {
		tx := types.Tx(fmt.Sprintf("tx%d", i))
		if withSequence {
			sequence = uint64(i)
		}
		wtx := newWrappedTx(tx.ToCachedTx(), int64(i), int64(i), int64(i), sender, sequence)
		txs = append(txs, wtx)
	}
	return txs
}

// Test helper functions
func reverseSlice(txs []*wrappedTx) []*wrappedTx {
	reversed := make([]*wrappedTx, len(txs))
	copy(reversed, txs)
	for i := 0; i < len(reversed)/2; i++ {
		reversed[i], reversed[len(reversed)-i-1] = reversed[len(reversed)-i-1], reversed[i]
	}
	return reversed
}

func shuffleSlice(txs []*wrappedTx) []*wrappedTx {
	shuffled := make([]*wrappedTx, len(txs))
	copy(shuffled, txs)
	rand.Shuffle(len(shuffled), func(i, j int) {
		shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
	})
	return shuffled
}

// captureStoreState captures the state of the store at a given
func captureStoreState(store *store) (size int, totalBytes int64, orderedTxSets int) {
	return store.size(), store.totalBytes(), len(store.orderedTxSets)
}

func verifyRemoval(t *testing.T, store *store, removedTx *wrappedTx, expectedOrder []*wrappedTx, initialSize int, initialBytes int64, initialSets int) {
	// Verify order is maintained
	require.Equal(t, expectedOrder, store.getOrderedTxs())

	// Verify transaction is completely removed
	require.Nil(t, store.get(removedTx.key()))
	require.False(t, store.has(removedTx.key()))
	require.False(t, store.isReserved(removedTx.key()))

	// Verify store state is updated correctly
	require.Equal(t, initialSize-1, store.size())
	require.Equal(t, initialBytes-removedTx.size(), store.totalBytes())
	require.Equal(t, initialSets-1, len(store.orderedTxSets))

	// Verify transaction is not in any signer sets
	for _, set := range store.setsBySigner {
		for _, tx := range set.txs {
			require.NotEqual(t, tx.key(), removedTx.key())
		}
	}
}

func verifyEmptyStore(t *testing.T, store *store) {
	require.Equal(t, 0, len(store.setsBySigner))
	require.Equal(t, 0, len(store.orderedTxSets))
	require.Equal(t, 0, len(store.txs))
	require.Equal(t, 0, len(store.getAllKeys()))
	require.Equal(t, 0, len(store.getOrderedTxs()))
	require.Equal(t, 0, store.size())
	require.Equal(t, int64(0), store.totalBytes())
}

func TestStore(t *testing.T) {
	t.Run("remove() maintains order and cleans up properly", func(t *testing.T) {
		store := newStore()
		const numTxs = 100

		// Create and add transactions in shuffled order
		originalTxs := makeTxs(numTxs, nil, false)
		expectedOrder := reverseSlice(originalTxs)
		shuffledTxs := shuffleSlice(originalTxs)

		for _, wtx := range shuffledTxs {
			store.set(wtx)
		}
		require.Equal(t, expectedOrder, store.getOrderedTxs())

		// Test single removal
		initialSize, initialBytes, initialSets := captureStoreState(store)
		txToRemove := expectedOrder[30]

		removed := store.remove(txToRemove.key())
		require.True(t, removed)

		expectedOrder = append(expectedOrder[:30], expectedOrder[31:]...)
		verifyRemoval(t, store, txToRemove, expectedOrder, initialSize, initialBytes, initialSets)

		// Test second removal to verify order maintenance
		secondTxToRemove := expectedOrder[60]
		removed = store.remove(secondTxToRemove.key())
		require.True(t, removed)

		expectedOrder = append(expectedOrder[:60], expectedOrder[61:]...)
		require.Equal(t, expectedOrder, store.getOrderedTxs())

		// Test duplicate removal returns false
		removed = store.remove(txToRemove.key())
		require.False(t, removed)

		// Test complete cleanup
		for _, wtx := range expectedOrder {
			store.remove(wtx.key())
		}

		// check that those tx sets were removed
		require.Equal(t, 0, len(store.setsBySigner))
		require.Equal(t, 0, len(store.orderedTxSets))
		require.Equal(t, 0, len(store.txs))
		require.Equal(t, 0, len(store.getAllKeys()))
		require.Equal(t, 0, len(store.getOrderedTxs()))
	})
	t.Run("remove() non-empty signer set", func(t *testing.T) {
		store := newStore()

		// Create transactions from same signer with sequences 1-10
		txs := makeTxs(10, []byte("signer1"), true)
		for _, wtx := range txs {
			store.set(wtx)
		}

		// Verify initial state - all transactions should be in order by sequence
		orderedTxs := store.getOrderedTxs()
		require.Equal(t, txs, orderedTxs)
		require.Equal(t, 1, len(store.setsBySigner)) // One signer set
		require.Equal(t, 10, len(store.setsBySigner[string([]byte("signer1"))].txs))

		// Test removing middle transaction (sequence 6)
		initialSize, initialBytes, initialSets := captureStoreState(store)
		txToRemove := txs[5] // sequence 6

		removed := store.remove(txToRemove.key())
		require.True(t, removed)

		// Verify signer set still exists but has one less transaction
		signerSet := store.setsBySigner[string([]byte("signer1"))]
		require.NotNil(t, signerSet)
		require.Equal(t, 9, len(signerSet.txs))

		// Verify sequence order is maintained and transaction was removed
		remainingTxs := store.getOrderedTxs()
		require.Equal(t, 9, len(remainingTxs))           // One less transaction
		require.NotContains(t, remainingTxs, txToRemove) // Removed tx not in list

		// Verify sequences are still in order
		for i := 0; i < 5; i++ {
			require.Equal(t, uint64(i+1), remainingTxs[i].sequence)
		}
		for i := 5; i < 9; i++ {
			require.Equal(t, uint64(i+2), remainingTxs[i].sequence)
		}

		// Verify store state
		require.Equal(t, initialSize-1, store.size())
		require.Equal(t, initialBytes-txToRemove.size(), store.totalBytes())
		require.Equal(t, initialSets, len(store.orderedTxSets)) // Same number of sets

		// Test removing first transaction (sequence 1)
		firstTx := remainingTxs[0]
		removed = store.remove(firstTx.key())
		require.True(t, removed)

		remainingTxs = store.getOrderedTxs()
		require.Equal(t, 8, len(remainingTxs))
		require.Equal(t, uint64(2), remainingTxs[0].sequence) // First is now sequence 2
		require.Equal(t, 8, len(store.setsBySigner[string([]byte("signer1"))].txs))

		// Test removing last transaction (sequence 10)
		lastTx := remainingTxs[len(remainingTxs)-1]
		require.Equal(t, uint64(10), lastTx.sequence) // Verify it's sequence 10
		removed = store.remove(lastTx.key())
		require.True(t, removed)

		remainingTxs = store.getOrderedTxs()
		require.Equal(t, 7, len(remainingTxs))
		require.Equal(t, uint64(9), remainingTxs[len(remainingTxs)-1].sequence) // Last is now sequence 9
		require.Equal(t, 7, len(store.setsBySigner[string([]byte("signer1"))].txs))

		// Remove all remaining transactions
		for _, wtx := range remainingTxs {
			store.remove(wtx.key())
		}

		// Verify signer set is completely cleaned up
		require.Equal(t, 0, len(store.setsBySigner))
		verifyEmptyStore(t, store)
	})
}

func TestStoreReservingTxs(t *testing.T) {
	store := newStore()

	tx := types.Tx("tx1")
	key := tx.Key()
	wtx := newWrappedTx(tx.ToCachedTx(), 1, 1, 1, nil, 0)

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
	require.Len(t, store.getOrderedTxs(), 0)
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
				wtx := newWrappedTx(tx.ToCachedTx(), 1, 1, 1, nil, 0)
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
		wtx := newWrappedTx(tx.ToCachedTx(), 1, 1, int64(i), nil, 0)
		store.set(wtx)
	}

	require.Equal(t, numTxs, store.size())

	// get all txs
	txs := store.getOrderedTxs()
	require.Equal(t, numTxs, len(txs))

	// get txs by keys
	keys := store.getAllKeys()
	require.Equal(t, numTxs, len(keys))

	// get sets below a certain priority and compute totals
	sets, bz := store.getTxSetsBelowPriority(int64(numTxs / 2))
	require.Equal(t, numTxs/2, len(sets))
	var actualBz int64
	countTxs := 0
	for _, set := range sets {
		actualBz += set.bytes
		countTxs += len(set.txs)
	}
	require.Equal(t, numTxs/2, countTxs)
	require.Equal(t, actualBz, bz)
}

func TestStoreExpiredTxs(t *testing.T) {
	store := newStore()
	numTxs := 100
	for i := 0; i < numTxs; i++ {
		tx := types.Tx(fmt.Sprintf("tx%d", i))
		wtx := newWrappedTx(tx.ToCachedTx(), int64(i), 1, 1, nil, 0)
		require.True(t, store.set(wtx))
	}

	require.Equal(t, numTxs, store.size())

	// half of them should get purged (by height). We assert the property instead of exact count,
	// because sets and ordering may drop additional txs due to reordering while mutating.
	_, purged := store.purgeExpiredTxs(int64(numTxs/2), time.Time{})
	require.Equal(t, numTxs/2, purged)

	remainingTxs := store.getOrderedTxs()
	for _, tx := range remainingTxs {
		require.GreaterOrEqual(t, tx.height, int64(numTxs/2))
	}

	store.purgeExpiredTxs(int64(0), time.Now().Add(time.Second))
	require.Empty(t, store.getOrderedTxs())
}

func TestPurgeExpiredTxs(t *testing.T) {
	store := newStore()
	signer := []byte("signer1")

	// Add two txs with the same signer, different heights
	wtx1 := newWrappedTx(types.Tx("tx1").ToCachedTx(), 4, 1, 10, signer, 1) // height 5
	wtx2 := newWrappedTx(types.Tx("tx2").ToCachedTx(), 5, 1, 10, signer, 2) // height 6

	require.True(t, store.set(wtx1))
	require.True(t, store.set(wtx2))

	// Both txs should be present
	require.Equal(t, 2, store.size())
	set := store.setsBySigner[string(signer)]
	require.NotNil(t, set)
	require.Equal(t, 2, len(set.txs))

	// Purge with expirationHeight = 5 (so tx1 is expired, tx2 is not)
	_, purged := store.purgeExpiredTxs(5, time.Time{})

	// The entire set should be removed, so both tx1 and tx2 are purged
	require.Equal(t, 2, purged)
	require.Equal(t, 0, store.size())
	require.Nil(t, store.setsBySigner[string(signer)])
	require.Empty(t, store.getOrderedTxs())
}

func TestStoreGetOrderedTxsWithoutSigner(t *testing.T) {
	store := newStore()
	numTxs := 10
	// Add transactions with different priorities to test ordering
	priorities := []int64{5, 1, 8, 3, 7, 2, 6, 4, 9, 0}

	for i, priority := range priorities {
		tx := types.Tx(fmt.Sprintf("tx%d", i))
		cachedTx := &types.CachedTx{Tx: tx}
		wtx := newWrappedTx(cachedTx, 1, 1, priority, nil, 0)
		store.set(wtx)
	}

	// Get all ordered transactions
	orderedTxs := store.getOrderedTxs()
	require.Equal(t, numTxs, len(orderedTxs))

	// Verify they are ordered by priority (highest first)
	for i := 1; i < len(orderedTxs); i++ {
		require.GreaterOrEqual(t, orderedTxs[i-1].priority, orderedTxs[i].priority,
			"Transactions should be ordered by priority (highest first)")
	}

	// Verify the returned slice is a copy (modifying it doesn't affect the store)
	originalLen := len(orderedTxs)
	orderedTxs[0] = nil

	newOrderedTxs := store.getOrderedTxs()
	require.Equal(t, originalLen, len(newOrderedTxs))
	require.NotNil(t, newOrderedTxs[0], "Original store data should not be affected by modifying the returned slice")
}

func TestStoreGetOrderedTxs_MultiSignerPriorityAndSequence(t *testing.T) {
	store := newStore()
	numSigners := 5
	txsPerSigner := 4

	// We'll use signers "signer0", "signer1", ..., "signer4"
	signers := make([][]byte, numSigners)
	for i := 0; i < numSigners; i++ {
		signers[i] = []byte(fmt.Sprintf("signer%d", i))
	}

	// For each signer, add txsPerSigner transactions with increasing sequence and decreasing priority
	for s := 0; s < numSigners; s++ {
		for seq := 1; seq <= txsPerSigner; seq++ {
			// Priority: start high for signer0, lower for signer1, etc.
			priority := rand.Int63n(100)       // pick a random priority
			gasWanted := rand.Int63n(1000) + 1 // pick a random gas wanted (not used here, but could be)
			tx := types.Tx(fmt.Sprintf("tx_signer%d_seq%d", s, seq))
			cachedTx := &types.CachedTx{Tx: tx}
			wtx := newWrappedTx(cachedTx, 1, gasWanted, priority, signers[s], uint64(seq))
			store.set(wtx)
		}
	}

	// Get all ordered transactions
	orderedTxs := store.getOrderedTxs()

	// There should be numSigners * txsPerSigner transactions
	require.Equal(t, numSigners*txsPerSigner, len(orderedTxs))

	// Group transactions by signer for sequence check
	signerSeqs := make(map[string][]uint64)
	// Track the last set priority to check ordering between sets
	var lastSetPriority *int64
	var lastSetSigner string

	for i, wtx := range orderedTxs {
		signer := string(wtx.sender)
		signerSeqs[signer] = append(signerSeqs[signer], wtx.sequence)

		// Find the set for this signer
		set := store.setsBySigner[signer]
		require.NotNil(t, set, "set for signer %s should exist", signer)

		// If this is the first tx or a new set (signer), check set priority ordering
		if i == 0 || signer != lastSetSigner {
			if lastSetPriority != nil {
				// The set priority should be strictly decreasing (higher first)
				require.GreaterOrEqual(t, *lastSetPriority, set.aggregatedPriority,
					"Tx sets should be ordered by decreasing aggregated priority")
			}
			lastSetPriority = &set.aggregatedPriority
			lastSetSigner = signer
		}
	}

	// Now, for each signer, check that sequence numbers are strictly increasing
	for signer, seqs := range signerSeqs {
		for i := 1; i < len(seqs); i++ {
			require.Greater(t, seqs[i], seqs[i-1],
				"Sequence numbers for signer %s should be strictly increasing", signer)
		}
	}
}

func TestAggregatedPriorityWeightedByGas(t *testing.T) {
	store := newStore()

	signer := []byte("addr1")
	// First tx: high priority, low gas
	w1 := newWrappedTx(types.Tx("a1").ToCachedTx(), 1, 1, 10, signer, 1)
	store.set(w1)

	// Second tx: lower priority, higher gas
	w2 := newWrappedTx(types.Tx("a2").ToCachedTx(), 1, 3, 4, signer, 2)
	store.set(w2)

	set := store.setsBySigner[string(signer)]
	require.NotNil(t, set)
	// Weighted average = (10*1 + 4*3) / (1+3) = 22/4 = 5 (int division)
	require.Equal(t, int64(5), set.aggregatedPriority)
}

func TestAggregatedPriorityAfterAdd(t *testing.T) {
	store := newStore()
	signer := []byte("addr1")
	w1 := newWrappedTx(types.Tx("a1").ToCachedTx(), 1, 1, 10, signer, 1)
	store.set(w1)
	w2 := newWrappedTx(types.Tx("a2").ToCachedTx(), 1, 3, 4, signer, 2)
	store.set(w2)

	// New candidate tx
	cand := newWrappedTx(types.Tx("a3").ToCachedTx(), 1, 1, 9, signer, 3)
	newAgg := store.aggregatedPriorityAfterAdd(cand)
	// Current weighted sum = 22, totalGas=4; after add: (22 + 9*1)/(4+1) = 31/5 = 6
	require.Equal(t, int64(6), newAgg)
}

func TestIntraSetOrderingBySequenceThenTimestamp(t *testing.T) {
	store := newStore()
	signer := []byte("addr1")

	// Add sequence 2 first
	w2a := newWrappedTx(types.Tx("s2a").ToCachedTx(), 1, 1, 1, signer, 2)
	store.set(w2a)
	// Ensure a different timestamp for tie-breaker
	time.Sleep(5 * time.Millisecond)
	// Add sequence 1
	w1 := newWrappedTx(types.Tx("s1").ToCachedTx(), 1, 1, 1, signer, 1)
	store.set(w1)
	// Add another sequence 2 later
	time.Sleep(5 * time.Millisecond)
	w2b := newWrappedTx(types.Tx("s2b").ToCachedTx(), 1, 1, 1, signer, 2)
	store.set(w2b)

	ordered := store.getOrderedTxs()
	require.Equal(t, 3, len(ordered))
	// Expect sequence 1 first, then the earlier seq=2 (w2a), then later seq=2 (w2b)
	require.Equal(t, types.Tx("s1"), ordered[0].tx.Tx)
	require.Equal(t, types.Tx("s2a"), ordered[1].tx.Tx)
	require.Equal(t, types.Tx("s2b"), ordered[2].tx.Tx)
}

func TestInterSetOrderingByAggregatedPriorityAndTimestamp(t *testing.T) {
	store := newStore()
	// Signer A: aggregated priority becomes 6 (from previous example)
	A := []byte("A")
	store.set(newWrappedTx(types.Tx("a1").ToCachedTx(), 1, 1, 10, A, 1))
	store.set(newWrappedTx(types.Tx("a2").ToCachedTx(), 1, 3, 4, A, 2))

	// Small pause so A's firstTimestamp is earlier
	time.Sleep(5 * time.Millisecond)

	// Signer B: choose values to get aggregated priority 5
	B := []byte("B")
	store.set(newWrappedTx(types.Tx("b1").ToCachedTx(), 1, 2, 4, B, 1)) // sum=8, gas=2
	store.set(newWrappedTx(types.Tx("b2").ToCachedTx(), 1, 2, 6, B, 2)) // sum=20, gas=4 => 20/4 = 5

	ordered := store.getOrderedTxs()
	// Expect all A txs (seq 1 then 2) before all B txs
	require.Equal(t, types.Tx("a1"), ordered[0].tx.Tx)
	require.Equal(t, types.Tx("a2"), ordered[1].tx.Tx)
	require.Equal(t, types.Tx("b1"), ordered[2].tx.Tx)
	require.Equal(t, types.Tx("b2"), ordered[3].tx.Tx)

	// Now make B match A's aggregated priority (set to 6) and add a new set C with same agg but later timestamp
	store = newStore()
	A = []byte("A")
	store.set(newWrappedTx(types.Tx("a1").ToCachedTx(), 1, 1, 10, A, 1))
	store.set(newWrappedTx(types.Tx("a2").ToCachedTx(), 1, 3, 4, A, 2)) // A agg = 6
	// A is earlier
	time.Sleep(5 * time.Millisecond)
	C := []byte("C")
	store.set(newWrappedTx(types.Tx("c1").ToCachedTx(), 1, 1, 10, C, 1))
	store.set(newWrappedTx(types.Tx("c2").ToCachedTx(), 1, 3, 4, C, 2)) // C agg = 6, later firstTimestamp

	ordered = store.getOrderedTxs()
	// A's set should come before C's due to earlier firstTimestamp
	require.Equal(t, types.Tx("a1"), ordered[0].tx.Tx)
	require.Equal(t, types.Tx("a2"), ordered[1].tx.Tx)
	require.Equal(t, types.Tx("c1"), ordered[2].tx.Tx)
	require.Equal(t, types.Tx("c2"), ordered[3].tx.Tx)
}

func TestTxSetAddRemoveProperties(t *testing.T) {
	// Helper to create a wrappedTx with given params and a fixed timestamp
	makeTx := func(tx string, height int64, gasWanted int64, priority int64, signer []byte, seq uint64, ts time.Time) *wrappedTx {
		w := newWrappedTx(types.Tx(tx).ToCachedTx(), height, gasWanted, priority, signer, seq)
		w.timestamp = ts
		return w
	}

	signer := []byte("S")
	baseTime := time.Now()

	// Create txs with different sequence, priority, gas, and timestamps
	w1 := makeTx("tx1", 10, 2, 10, signer, 1, baseTime.Add(1*time.Second)) // seq=1, prio=10, gas=2, ts=+1s
	w2 := makeTx("tx2", 12, 3, 4, signer, 2, baseTime.Add(2*time.Second))  // seq=2, prio=4, gas=3, ts=+2s
	w3 := makeTx("tx3", 15, 5, 6, signer, 3, baseTime.Add(3*time.Second))  // seq=3, prio=6, gas=5, ts=+3s

	// Initialize set with one tx, then add others out of order
	set := newTxSet(w2)
	set.addTxToSet(w1)
	set.addTxToSet(w3)

	// After all added, txs should be sorted by sequence
	require.Equal(t, uint64(1), set.txs[0].sequence)
	require.Equal(t, uint64(2), set.txs[1].sequence)
	require.Equal(t, uint64(3), set.txs[2].sequence)

	// firstTimestamp should be from w1 (seq=1, ts=+1s)
	require.True(t, set.firstTimestamp.Equal(w1.timestamp))

	// Aggregated priority (gas-weighted): (10*2 + 4*3 + 6*5)/(2+3+5) = 62/10 = 6
	require.Equal(t, int64(6), set.aggregatedPriority)

	// Remove the first tx (seq=1)
	_ = set.removeTx(w1)
	require.Equal(t, 2, len(set.txs))
	// Now firstTimestamp should be from w2 (seq=2, ts=+2s)
	require.True(t, set.firstTimestamp.Equal(w2.timestamp))
	// Aggregated priority: (4*3 + 6*5)/(3+5) = 42/8 = 5
	require.Equal(t, int64(5), set.aggregatedPriority)

	// Remove the next tx (seq=2)
	_ = set.removeTx(w2)
	require.Equal(t, 1, len(set.txs))
	// Now firstTimestamp should be from w3 (seq=3, ts=+3s)
	require.True(t, set.firstTimestamp.Equal(w3.timestamp))
	// Aggregated priority: 6*5/5 = 6
	require.Equal(t, int64(6), set.aggregatedPriority)

	// Remove last tx
	_ = set.removeTx(w3)
	require.Equal(t, 0, len(set.txs))
	// firstTimestamp should be reset; aggregatedPriority should be zero
	require.True(t, set.firstTimestamp.IsZero())
	require.Equal(t, int64(0), set.aggregatedPriority)
}

func TestTxSetFirstHeightRecalculationOnRemove(t *testing.T) {
	signer := []byte("alice")

	// Create txs at different heights
	tx1 := newWrappedTx(types.Tx("tx1").ToCachedTx(), 100, 1, 10, signer, 1) // height=100
	tx2 := newWrappedTx(types.Tx("tx2").ToCachedTx(), 150, 1, 10, signer, 2) // height=150
	tx3 := newWrappedTx(types.Tx("tx3").ToCachedTx(), 120, 1, 10, signer, 3) // height=120

	set := newTxSet(tx1)
	require.Equal(t, int64(100), set.firstHeight)

	// Add tx2 (height=150) - firstHeight should remain 100
	set.addTxToSet(tx2)
	require.Equal(t, int64(100), set.firstHeight)

	// Add tx3 (height=120) - firstHeight should remain 100
	set.addTxToSet(tx3)
	require.Equal(t, int64(100), set.firstHeight)

	// Remove tx1 (the one with lowest height) - firstHeight should update to 120 (tx3)
	removed := set.removeTx(tx1)
	require.True(t, removed)
	require.Equal(t, int64(120), set.firstHeight, "firstHeight should be recalculated to lowest remaining height")

	// Remove tx3 (now the lowest) - firstHeight should update to 150 (tx2)
	removed = set.removeTx(tx3)
	require.True(t, removed)
	require.Equal(t, int64(150), set.firstHeight, "firstHeight should be recalculated after removing lowest")

	// Remove last tx - firstHeight should reset to 0
	removed = set.removeTx(tx2)
	require.True(t, removed)
	require.Equal(t, int64(0), set.firstHeight, "firstHeight should be 0 when set is empty")
}
