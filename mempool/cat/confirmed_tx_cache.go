package cat

import (
	"container/heap"
	"sync"

	"github.com/cometbft/cometbft/types"
)

// confirmedTxEntry stores a transaction with the height at which it was added.
type confirmedTxEntry struct {
	txKey  types.TxKey
	tx     types.Tx
	height int64
	index  int // index in the heap
}

// confirmedTxHeap is a min-heap ordered by height (lowest height first).
type confirmedTxHeap []*confirmedTxEntry

func (h confirmedTxHeap) Len() int           { return len(h) }
func (h confirmedTxHeap) Less(i, j int) bool { return h[i].height < h[j].height }
func (h confirmedTxHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index = i
	h[j].index = j
}

func (h *confirmedTxHeap) Push(x any) {
	entry := x.(*confirmedTxEntry)
	entry.index = len(*h)
	*h = append(*h, entry)
}

func (h *confirmedTxHeap) Pop() any {
	old := *h
	n := len(old)
	entry := old[n-1]
	old[n-1] = nil
	entry.index = -1
	*h = old[0 : n-1]
	return entry
}

// confirmedTxCache stores transactions that passed CheckTx so they can still
// be served to peers who request them via WantTx after the tx has been
// removed from the mempool. This helps slow validators that are behind
// the network to still receive txs they request.
//
// The cache is limited by memory (maxBytes). When full, oldest entries
// (lowest heights) are evicted first.
type confirmedTxCache struct {
	mtx      sync.RWMutex
	txs      map[types.TxKey]*confirmedTxEntry
	heap     confirmedTxHeap
	bytes    int64
	maxBytes int64
}

// newConfirmedTxCache creates a new confirmed transaction cache with the given max bytes.
func newConfirmedTxCache(maxBytes int64) *confirmedTxCache {
	return &confirmedTxCache{
		txs:      make(map[types.TxKey]*confirmedTxEntry),
		heap:     make(confirmedTxHeap, 0),
		maxBytes: maxBytes,
	}
}

// Add adds a transaction to the cache with the given height.
// If adding would exceed maxBytes, oldest entries (lowest heights) are evicted.
func (c *confirmedTxCache) Add(txKey types.TxKey, tx types.Tx, height int64) {
	c.mtx.Lock()
	defer c.mtx.Unlock()

	// Don't add duplicates
	if _, exists := c.txs[txKey]; exists {
		return
	}

	txSize := int64(len(tx))

	// Evict oldest entries until we have room
	for c.bytes+txSize > c.maxBytes && c.heap.Len() > 0 {
		oldest := heap.Pop(&c.heap).(*confirmedTxEntry)
		c.bytes -= int64(len(oldest.tx))
		delete(c.txs, oldest.txKey)
	}

	// If single tx is larger than maxBytes, don't add it
	if txSize > c.maxBytes {
		return
	}

	entry := &confirmedTxEntry{
		txKey:  txKey,
		tx:     tx,
		height: height,
	}
	c.txs[txKey] = entry
	heap.Push(&c.heap, entry)
	c.bytes += txSize
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

// Size returns the number of entries in the cache.
func (c *confirmedTxCache) Size() int {
	c.mtx.RLock()
	defer c.mtx.RUnlock()
	return len(c.txs)
}

// Bytes returns the total bytes used by the cache.
func (c *confirmedTxCache) Bytes() int64 {
	c.mtx.RLock()
	defer c.mtx.RUnlock()
	return c.bytes
}

// Prune removes all entries with height less than minHeight.
func (c *confirmedTxCache) Prune(minHeight int64) {
	c.mtx.Lock()
	defer c.mtx.Unlock()

	// Pop entries from the heap while they're below minHeight
	for c.heap.Len() > 0 && c.heap[0].height < minHeight {
		oldest := heap.Pop(&c.heap).(*confirmedTxEntry)
		c.bytes -= int64(len(oldest.tx))
		delete(c.txs, oldest.txKey)
	}
}
