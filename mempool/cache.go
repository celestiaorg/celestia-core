package mempool

import (
	"container/list"

	cmtsync "github.com/cometbft/cometbft/libs/sync"
	"github.com/cometbft/cometbft/types"
)

// TxCache defines an interface for raw transaction caching in a mempool.
// Currently, a TxCache does not allow direct reading or getting of transaction
// values. A TxCache is used primarily to push transactions and removing
// transactions. Pushing via Push returns a boolean telling the caller if the
// transaction already exists in the cache or not.
type TxCache interface {
	// Reset resets the cache to an empty state.
	Reset()

	// Push adds the given raw transaction to the cache and returns true if it was
	// newly added. Otherwise, it returns false.
	Push(tx *types.CachedTx) bool

	// Remove removes the given raw transaction from the cache.
	Remove(tx *types.CachedTx)

	// Has reports whether tx is present in the cache. Checking for presence is
	// not treated as an access of the value.
	Has(tx *types.CachedTx) bool

	// HasKey reports whether the given key is present in the cache.
	HasKey(key types.TxKey) bool
}

var _ TxCache = (*LRUTxCache)(nil)

// LRUTxCache maintains a thread-safe LRU cache of raw transactions. The cache
// only stores the hash of the raw transaction.
type LRUTxCache struct {
	mtx      cmtsync.Mutex
	size     int
	cacheMap map[types.TxKey]*list.Element
	list     *list.List
}

// cacheEntry stores both the transaction key and optional error code
type cacheEntry struct {
	key  types.TxKey
	code uint32
}

func NewLRUTxCache(cacheSize int) *LRUTxCache {
	return &LRUTxCache{
		size:     cacheSize,
		cacheMap: make(map[types.TxKey]*list.Element, cacheSize),
		list:     list.New(),
	}
}

// GetList returns the underlying linked-list that backs the LRU cache. Note,
// this should be used for testing purposes only!
func (c *LRUTxCache) GetList() *list.List {
	return c.list
}

func (c *LRUTxCache) Reset() {
	c.mtx.Lock()
	defer c.mtx.Unlock()

	c.cacheMap = make(map[types.TxKey]*list.Element, c.size)
	c.list.Init()
}

func (c *LRUTxCache) Push(tx *types.CachedTx) bool {
	c.mtx.Lock()
	defer c.mtx.Unlock()

	key := tx.Key()

	moved, ok := c.cacheMap[key]
	if ok {
		c.list.MoveToBack(moved)
		return false
	}

	if c.list.Len() >= c.size {
		front := c.list.Front()
		if front != nil {
			frontKey := front.Value.(types.TxKey)
			delete(c.cacheMap, frontKey)
			c.list.Remove(front)
		}
	}

	e := c.list.PushBack(key)
	c.cacheMap[key] = e

	return true
}

func (c *LRUTxCache) Remove(tx *types.CachedTx) {
	key := tx.Key()
	c.RemoveTxByKey(key)
}

func (c *LRUTxCache) RemoveTxByKey(key types.TxKey) {
	c.mtx.Lock()
	defer c.mtx.Unlock()

	e := c.cacheMap[key]
	delete(c.cacheMap, key)

	if e != nil {
		c.list.Remove(e)
	}
}

func (c *LRUTxCache) Has(tx *types.CachedTx) bool {
	c.mtx.Lock()
	defer c.mtx.Unlock()

	_, ok := c.cacheMap[tx.Key()]
	return ok
}

func (c *LRUTxCache) HasKey(key types.TxKey) bool {
	c.mtx.Lock()
	defer c.mtx.Unlock()

	_, ok := c.cacheMap[key]
	return ok
}

// PushWithCode pushes a transaction to the cache and sets the code for the transaction
func (c *LRUTxCache) PushWithCode(tx *types.CachedTx, code uint32) bool {
	c.mtx.Lock()
	defer c.mtx.Unlock()

	key := tx.Key()

	moved, ok := c.cacheMap[key]
	if ok {
		c.list.MoveToBack(moved)
		return false
	}

	if c.list.Len() >= c.size {
		front := c.list.Front()
		if front != nil {
			frontKey := front.Value.(cacheEntry).key
			delete(c.cacheMap, frontKey)
			c.list.Remove(front)
		}
	}

	e := c.list.PushBack(cacheEntry{key: key, code: code})
	c.cacheMap[key] = e

	return true
}

// GetRejectionCode returns the error code for a transaction if it exists in the cache
func (c *LRUTxCache) GetRejectionCode(key types.TxKey) (uint32, bool) {
	c.mtx.Lock()
	defer c.mtx.Unlock()

	 // return key and code
	 entry, ok := c.cacheMap[key]
	 if !ok {
		return 0, false
	 }

	 return entry.Value.(cacheEntry).code, true
}

// NopTxCache defines a no-op raw transaction cache.
type NopTxCache struct{}

var _ TxCache = (*NopTxCache)(nil)

func (NopTxCache) Reset()                    {}
func (NopTxCache) Push(*types.CachedTx) bool { return true }
func (NopTxCache) Remove(*types.CachedTx)    {}
func (NopTxCache) Has(*types.CachedTx) bool  { return false }
func (NopTxCache) HasKey(types.TxKey) bool   { return false }
