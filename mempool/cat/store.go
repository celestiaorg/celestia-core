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

// txSet groups transactions from the same signer and carries an aggregated priority.
// Transactions within a set are ordered by sequence (ascending). If sequences are
// equal or unset, order by arrival timestamp.
type txSet struct {
	signerKey string
	signer    []byte
	// this should be ordered by sequence (ascending)
	txs                []*wrappedTx
	aggregatedPriority int64
	bytes              int64
	firstTimestamp     time.Time
	firstHeight        int64
	// gas-weighted aggregation tracking
	totalGasWanted      int64
	weightedPrioritySum int64
}

func newTxSet(wtx *wrappedTx) *txSet {
	txSet := &txSet{
		signerKey: string(wtx.sender),
		signer:    wtx.sender,
		txs:       make([]*wrappedTx, 0, 1),
	}
	txSet.addTxToSet(wtx)
	return txSet
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
	_ = set.removeTx(tx)
	// If the set is empty, remove it.
	if len(set.txs) == 0 {
		delete(s.setsBySigner, signerKey)
		return true
	}
	// If the set is not empty, reorder it.
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

func (s *store) getAllTxs() []*wrappedTx {
	s.mtx.RLock()
	defer s.mtx.RUnlock()

	txs := make([]*wrappedTx, len(s.txs))
	idx := 0
	for _, tx := range s.txs {
		txs[idx] = tx
		idx++
	}
	return txs
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
		if set.aggregatedPriority > priority {
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
	idx := s.getSetOrder(ts)
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
func (s *store) getSetOrder(ts *txSet) int {
	return sort.Search(len(s.orderedTxSets), func(i int) bool {
		if s.orderedTxSets[i].aggregatedPriority == ts.aggregatedPriority {
			return ts.firstTimestamp.Before(s.orderedTxSets[i].firstTimestamp)
		}
		return s.orderedTxSets[i].aggregatedPriority < ts.aggregatedPriority
	})
}

func (s *store) iterateOrderedTxs(fn func(tx *wrappedTx) bool) {
	s.mtx.RLock()
	defer s.mtx.RUnlock()

	for _, set := range s.orderedTxSets {
		for _, tx := range set.txs {
			if !fn(tx) {
				return
			}
		}
	}
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

// addTxToSet inserts wtx into the set maintaining sequence order (ascending).
// If sequence equal, order by timestamp.
func (set *txSet) addTxToSet(wtx *wrappedTx) {
	idx := sort.Search(len(set.txs), func(i int) bool {
		if set.txs[i].sequence == wtx.sequence {
			return wtx.timestamp.Before(set.txs[i].timestamp)
		}
		return set.txs[i].sequence > wtx.sequence
	})
	if idx >= len(set.txs) {
		set.txs = append(set.txs, wtx)
	} else {
		set.txs = append(set.txs[:idx], append([]*wrappedTx{wtx}, set.txs[idx:]...)...)
	}

	// update gas-weighted aggregation
	set.totalGasWanted += wtx.gasWanted
	set.weightedPrioritySum += wtx.priority * wtx.gasWanted
	if set.totalGasWanted > 0 {
		set.aggregatedPriority = set.weightedPrioritySum / set.totalGasWanted
	}
	set.bytes += wtx.size()
	if set.firstTimestamp.IsZero() || wtx.timestamp.Before(set.firstTimestamp) {
		set.firstTimestamp = wtx.timestamp
	}
	if set.firstHeight == 0 || wtx.height < set.firstHeight {
		set.firstHeight = wtx.height
	}
}

// removeTx removes the provided wrappedTx from the set and updates aggregation.
func (set *txSet) removeTx(wtx *wrappedTx) bool {
	for i, tx := range set.txs {
		if tx == wtx {
			set.bytes -= wtx.size()
			set.totalGasWanted -= wtx.gasWanted
			set.weightedPrioritySum -= wtx.priority * wtx.gasWanted
			// Remove the tx from the set
			set.txs = append(set.txs[:i], set.txs[i+1:]...)

			// If the set is empty, or the total gas wanted is zero
			// set the aggregated priority to zero
			if len(set.txs) <= 0 || set.totalGasWanted <= 0 {
				set.aggregatedPriority = 0
				set.firstTimestamp = time.Time{}
				return true
			}

			// Recompute earliest timestamp
			earliest := set.txs[0].timestamp
			for _, t := range set.txs {
				if t.timestamp.Before(earliest) {
					earliest = t.timestamp
				}
			}
			set.firstTimestamp = earliest
			set.aggregatedPriority = set.weightedPrioritySum / set.totalGasWanted
			return true
		}
	}
	return false
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
