package cat

import (
	"sort"
	"time"

	"github.com/cometbft/cometbft/types"
)

// wrappedTx defines a wrapper around a raw transaction with additional metadata
// that is used for indexing. With the exception of the map of peers who have
// seen this transaction, this struct should never be modified
type wrappedTx struct {
	// these fields are immutable
	tx        *types.CachedTx // the original transaction data
	height    int64           // height when this transaction was initially checked (for expiry)
	timestamp time.Time       // time when transaction was entered (for TTL)
	gasWanted int64           // app: gas required to execute this transaction
	priority  int64           // app: priority value for this transaction
	sender    []byte          // app: assigned sender label
	sequence  uint64          // app: sequence number for this transaction
}

func newWrappedTx(tx *types.CachedTx, height, gasWanted, priority int64, sender []byte, sequence uint64) *wrappedTx {
	return &wrappedTx{
		tx:        tx,
		height:    height,
		timestamp: time.Now().UTC(),
		gasWanted: gasWanted,
		priority:  priority,
		sender:    sender,
		sequence:  sequence,
	}
}

// Size reports the size of the raw transaction in bytes.
func (w *wrappedTx) size() int64 { return int64(len(w.tx.Tx)) }

// key returns the underlying tx key.
func (w *wrappedTx) key() types.TxKey { return w.tx.Key() }

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

func newTxSet(wtx ...*wrappedTx) *txSet {
	txSet := &txSet{}
	for _, wtx := range wtx {
		txSet.addTxToSet(wtx)
	}
	return txSet
}

// addTxToSet inserts wtx into the set maintaining sequence order (ascending).
// If sequence equal, order by timestamp.
func (set *txSet) addTxToSet(wtx *wrappedTx) {
	if len(set.txs) == 0 {
		set.txs = append(set.txs, wtx)
		set.signerKey = string(wtx.sender)
		set.signer = wtx.sender
		set.totalGasWanted += wtx.gasWanted
		set.weightedPrioritySum += wtx.priority * wtx.gasWanted
		set.bytes += wtx.size()
		set.firstTimestamp = wtx.timestamp
		set.firstHeight = wtx.height
		set.aggregatedPriority = set.weightedPrioritySum / set.totalGasWanted
		return
	}
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
	if wtx.timestamp.Before(set.firstTimestamp) {
		set.firstTimestamp = wtx.timestamp
	}
	if wtx.height < set.firstHeight {
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

// sliceTxsByBytesAndGas slices the transactions set into a possible subset
// that fits within the given bytes and gas constraints ordered by sequence
func (set *txSet) sliceTxsByBytesAndGas(numBytes int64, numGas int64) *txSet {
	// check if we have no budget left
	if numBytes == 0 || numGas == 0 {
		return newTxSet() // return empty set if no budget
	}
	// check if we have unlimited budget or enough budget for the whole set
	if (numBytes < 0 || numBytes >= set.bytes) && (numGas < 0 || numGas >= set.totalGasWanted) {
		return set // return full set if budget is sufficient
	}
	var (
		bytesUsed      int64
		totalGasWanted int64
	)
	txSet := newTxSet()
	for _, tx := range set.txs {
		txSize := tx.size()
		if bytesUsed+txSize > numBytes || numGas >= 0 && totalGasWanted+tx.gasWanted > numGas {
			break
		}
		txSet.addTxToSet(tx)
	}
	return txSet
}

func (set *txSet) rawTxs() []*types.CachedTx {
	txs := make([]*types.CachedTx, len(set.txs))
	for i, tx := range set.txs {
		txs[i] = tx.tx
	}
	return txs
}

func aggregatePriorityAcrossSets(txSets []*txSet) int64 {
	var (
		totalGasWanted      int64
		weightedPrioritySum int64
	)
	for _, txSet := range txSets {
		totalGasWanted += txSet.totalGasWanted
		weightedPrioritySum += txSet.weightedPrioritySum
	}
	if totalGasWanted > 0 {
		return weightedPrioritySum / totalGasWanted
	}
	return 0
}
