package cat

import (
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
