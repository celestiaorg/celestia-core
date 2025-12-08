package cat

import (
	"sync"

	"github.com/cometbft/cometbft/types"
)

const (
	// defaultConfirmedTxCacheHeightTTL is how many blocks to keep confirmed txs in cache.
	defaultConfirmedTxCacheHeightTTL = 15

	// defaultConfirmedTxCacheSize is the maximum number of confirmed txs to cache.
	defaultConfirmedTxCacheSize = 5000
)

// confirmedTxEntry stores a transaction with the height at which it was added.
type confirmedTxEntry struct {
	tx     types.Tx
	height int64
}

// confirmedTxCache stores transactions that passed CheckTx so they can still
// be served to peers who request them via WantTx after the tx has been
// removed from the mempool. This helps slow validators that are behind
// the network to still receive txs they request.
//
// Entries are cleaned up after a configurable number of blocks.
type confirmedTxCache struct {
	mtx       sync.RWMutex
	txs       map[types.TxKey]*confirmedTxEntry
	heightTTL int64
	maxSize   int
}

// newConfirmedTxCache creates a new confirmed transaction cache.
func newConfirmedTxCache() *confirmedTxCache {
	return &confirmedTxCache{
		txs:       make(map[types.TxKey]*confirmedTxEntry),
		heightTTL: defaultConfirmedTxCacheHeightTTL,
		maxSize:   defaultConfirmedTxCacheSize,
	}
}

// Add adds a transaction to the cache with the given height.
func (c *confirmedTxCache) Add(txKey types.TxKey, tx types.Tx, height int64) {
	c.mtx.Lock()
	defer c.mtx.Unlock()

	// If already at max size, don't add new entries
	// (cleanup happens on height advance)
	if len(c.txs) >= c.maxSize {
		return
	}

	c.txs[txKey] = &confirmedTxEntry{
		tx:     tx,
		height: height,
	}
}

// Get retrieves a transaction from the cache if it exists.
func (c *confirmedTxCache) Get(txKey types.TxKey) (types.Tx, bool) {
	c.mtx.RLock()
	defer c.mtx.RUnlock()

	entry, ok := c.txs[txKey]
	if !ok {
		return nil, false
	}

	return entry.tx, true
}

// PruneOlderThan removes all entries added at or before the given height.
func (c *confirmedTxCache) PruneOlderThan(height int64) {
	c.mtx.Lock()
	defer c.mtx.Unlock()

	for key, entry := range c.txs {
		if entry.height <= height {
			delete(c.txs, key)
		}
	}
}

// Size returns the number of entries in the cache.
func (c *confirmedTxCache) Size() int {
	c.mtx.RLock()
	defer c.mtx.RUnlock()
	return len(c.txs)
}
