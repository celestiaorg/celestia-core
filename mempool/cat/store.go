package cat

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/cometbft/cometbft/types"
)

// simple, thread-safe in memory store for transactions
type store struct {
	mtx         sync.RWMutex
	bytes       int64
	txs         map[types.TxKey]*wrappedTx
	reservedTxs map[types.TxKey]struct{}

	// signer-bundled tx sets ordered by aggregated priority
	setsBySigner  map[string]*txSet
	orderedTxSets []*txSet
}

func newStore() *store {
	return &store{
		bytes:         0,
		txs:           make(map[types.TxKey]*wrappedTx),
		reservedTxs:   make(map[types.TxKey]struct{}),
		setsBySigner:  make(map[string]*txSet),
		orderedTxSets: make([]*txSet, 0),
	}
}

func (s *store) set(wtx *wrappedTx) bool {
	if wtx == nil {
		return false
	}
	s.mtx.Lock()
	defer s.mtx.Unlock()
	if _, exists := s.txs[wtx.key()]; !exists {
		s.txs[wtx.key()] = wtx
		s.bytes += wtx.size()

		// If signer is empty use a single-tx set.
		signerKey := string(wtx.sender)
		if signerKey == "" {
			set := newTxSet(wtx)
			s.orderSet(set)
			return true
		}

		// Get or create the tx set
		set, exists := s.setsBySigner[signerKey]
		// If the tx set does not exist, create it.
		if !exists {
			set = newTxSet(wtx)
			s.setsBySigner[signerKey] = set
			s.orderSet(set)
			return true
		}

		// Remove existing set from ordered list, add tx to it
		// and reorder.
		if err := s.deleteOrderedSet(set); err != nil {
			panic(err)
		}
		set.addTxToSet(wtx)
		s.orderSet(set)
		return true
	}
	return false
}

func (s *store) get(txKey types.TxKey) *wrappedTx {
	s.mtx.RLock()
	defer s.mtx.RUnlock()
	return s.txs[txKey]
}

func (s *store) has(txKey types.TxKey) bool {
	s.mtx.RLock()
	defer s.mtx.RUnlock()
	_, has := s.txs[txKey]
	return has
}

func (s *store) getSetBySigner(signer []byte) *txSet {
	s.mtx.RLock()
	defer s.mtx.RUnlock()
	return s.setsBySigner[string(signer)]
}

// remove removes a transaction from the store.
func (s *store) remove(txKey types.TxKey) bool {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	tx, exists := s.txs[txKey]
	if !exists {
		return false
	}
	s.bytes -= tx.size()
	delete(s.txs, txKey)

	// Update the signer's set
	signerKey := string(tx.sender)
	if signerKey == "" {
		// Empty-signer sets are single-tx sets: remove the entire set containing tx
		for i, set := range s.orderedTxSets {
			if len(set.txs) == 1 && set.txs[0] == tx {
				s.orderedTxSets = append(s.orderedTxSets[:i], s.orderedTxSets[i+1:]...)
				return true
			}
		}
		// If the set is not found, return false.
		return false
	}

	set, ok := s.setsBySigner[signerKey]
	if !ok {
		return false
	}
	if err := s.deleteOrderedSet(set); err != nil {
		panic(err)
	}
	removed := set.removeTx(tx)
	if !removed {
		panic(fmt.Errorf("remove tx: transaction not found in set: %v", txKey))
	}
	// If the set is empty, remove it.
	if len(set.txs) == 0 {
		delete(s.setsBySigner, signerKey)
		return true
	}
	// If the set is not empty, readd and order it.
	s.orderSet(set)
	return true
}

// reserve adds an empty placeholder for the specified key to prevent
// a transaction with the same key from being added
func (s *store) reserve(txKey types.TxKey) bool {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	_, isReserved := s.reservedTxs[txKey]
	if !isReserved {
		s.reservedTxs[txKey] = struct{}{}
		return true
	}
	return false
}

func (s *store) isReserved(txKey types.TxKey) bool {
	s.mtx.RLock()
	defer s.mtx.RUnlock()
	_, isReserved := s.reservedTxs[txKey]
	return isReserved
}

// release is called at the end of the process of adding a transaction.
// Regardless if it is added or not, the reserveTxs lookup map element is deleted.
func (s *store) release(txKey types.TxKey) {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	delete(s.reservedTxs, txKey)
}

func (s *store) size() int {
	s.mtx.RLock()
	defer s.mtx.RUnlock()
	return len(s.txs)
}

func (s *store) totalBytes() int64 {
	s.mtx.RLock()
	defer s.mtx.RUnlock()
	return s.bytes
}

func (s *store) getAllKeys() []types.TxKey {
	s.mtx.RLock()
	defer s.mtx.RUnlock()

	keys := make([]types.TxKey, len(s.txs))
	idx := 0
	for key := range s.txs {
		keys[idx] = key
		idx++
	}
	return keys
}

// getTxSetsBelowPriority returns sets with aggregated priority below the given value
// starting from the lowest-priority set, and the cumulative bytes across those sets.
func (s *store) getTxSetsBelowPriority(priority int64) ([]*txSet, int64) {
	s.mtx.RLock()
	defer s.mtx.RUnlock()

	sets := make([]*txSet, 0, len(s.orderedTxSets))
	bytes := int64(0)
	for i := len(s.orderedTxSets) - 1; i >= 0; i-- {
		set := s.orderedTxSets[i]
		if set.aggregatedPriority >= priority {
			break
		}
		sets = append(sets, set)
		bytes += set.bytes
	}
	return sets, bytes
}

// purgeExpiredTxs removes all transactions that are older than the given height
// and time. Returns the purged txs and amount of transactions that were purged.
// This will also remove all transactions that have a higher sequence number
// which would now be invalid.
func (s *store) purgeExpiredTxs(expirationHeight int64, expirationAge time.Time) ([]*wrappedTx, int) {
	s.mtx.Lock()
	defer s.mtx.Unlock()

	var purgedTxs []*wrappedTx
	counter := 0

	txSets := make([]*txSet, len(s.orderedTxSets))
	copy(txSets, s.orderedTxSets)
	for _, set := range txSets {
		if set.firstHeight < expirationHeight || set.firstTimestamp.Before(expirationAge) {
			if err := s.deleteOrderedSet(set); err != nil {
				panic(err)
			}
			// Update the store's byte count.
			s.bytes -= set.bytes

			// Remove the set from the store.
			if set.signerKey != "" {
				delete(s.setsBySigner, set.signerKey)
			}

			// Clean up txs in the set and the store.
			for _, tx := range set.txs {
				purgedTxs = append(purgedTxs, tx)
				counter++
				// Remove the tx from the store.
				delete(s.txs, tx.key())
			}
		}
	}
	return purgedTxs, counter
}

func (s *store) reset() {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	s.bytes = 0
	s.txs = make(map[types.TxKey]*wrappedTx)
	s.setsBySigner = make(map[string]*txSet)
	s.orderedTxSets = make([]*txSet, 0)
}

// orderSet inserts the txSet into the orderedTxSets slice at the correct index
// based on its aggregated priority and timestamp.
func (s *store) orderSet(ts *txSet) {
	idx := s.getSetOrderIndex(ts)
	s.orderedTxSets = append(s.orderedTxSets[:idx], append([]*txSet{ts}, s.orderedTxSets[idx:]...)...)
}

// deleteOrderedSet removes the txSet from the orderedTxSets slice.
func (s *store) deleteOrderedSet(ts *txSet) error {
	if len(s.orderedTxSets) == 0 {
		return fmt.Errorf("ordered tx sets list is empty")
	}
	for i, set := range s.orderedTxSets {
		if set == ts {
			s.orderedTxSets = append(s.orderedTxSets[:i], s.orderedTxSets[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("tx set not found in ordered list: %v", ts)
}

// getSetOrder returns the index of the txSet in the orderedTxSets slice
// based on its aggregated priority and timestamp.
func (s *store) getSetOrderIndex(ts *txSet) int {
	return sort.Search(len(s.orderedTxSets), func(i int) bool {
		if s.orderedTxSets[i].aggregatedPriority == ts.aggregatedPriority {
			return ts.firstTimestamp.Before(s.orderedTxSets[i].firstTimestamp)
		}
		return s.orderedTxSets[i].aggregatedPriority < ts.aggregatedPriority
	})
}

// processOrderedTxSets processes the ordered tx sets in a thread-safe manner.
// no transactions can be added or removed from the store during this process.
func (s *store) processOrderedTxSets(fn func(txSets []*txSet)) {
	s.mtx.RLock()
	defer s.mtx.RUnlock()

	fn(s.orderedTxSets)
}

// getOrderedTxs returns a copy of all transactions in priority order.
// This method is safe to call concurrently and the returned slice can be
// processed without holding any store locks.
func (s *store) getOrderedTxs() []*wrappedTx {
	s.mtx.RLock()
	defer s.mtx.RUnlock()

	// Return a copy to avoid race conditions when the caller processes the slice
	total := 0
	for _, set := range s.orderedTxSets {
		total += len(set.txs)
	}
	txs := make([]*wrappedTx, 0, total)
	for _, set := range s.orderedTxSets {
		txs = append(txs, set.txs...)
	}
	return txs
}

// aggregatedPriorityAfterAdd computes the aggregated priority of the signer's set
// if this transaction were to be added.
func (s *store) aggregatedPriorityAfterAdd(wtx *wrappedTx) int64 {
	s.mtx.RLock()
	defer s.mtx.RUnlock()

	signerKey := string(wtx.sender)
	if signerKey == "" {
		// No existing set is tracked for empty signer; new set would have tx's priority
		return wtx.priority
	}
	if set, ok := s.setsBySigner[signerKey]; ok {
		newWeightedSum := set.weightedPrioritySum + (wtx.priority * wtx.gasWanted)
		newTotalGas := set.totalGasWanted + wtx.gasWanted
		if newTotalGas <= 0 {
			return 0
		}
		return newWeightedSum / newTotalGas
	}
	return wtx.priority
}
