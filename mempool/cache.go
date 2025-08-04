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
	Push(key types.TxKey) bool

	// Remove removes the given raw transaction from the cache.
	Remove(key types.TxKey)

	// Has reports whether tx key is present in the cache. Checking for presence is
	// not treated as an access of the value.
	Has(key types.TxKey) bool
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

func (c *LRUTxCache) Push(key types.TxKey) bool {
	c.mtx.Lock()
	defer c.mtx.Unlock()

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

func (c *LRUTxCache) Remove(key types.TxKey) {
	c.mtx.Lock()
	defer c.mtx.Unlock()

	e := c.cacheMap[key]
	delete(c.cacheMap, key)

	if e != nil {
		c.list.Remove(e)
	}
}

func (c *LRUTxCache) Has(key types.TxKey) bool {
	c.mtx.Lock()
	defer c.mtx.Unlock()

	_, ok := c.cacheMap[key]
	return ok
}

// NopTxCache defines a no-op raw transaction cache.
type NopTxCache struct{}

var _ TxCache = (*NopTxCache)(nil)

func (NopTxCache) Reset()                  {}
func (NopTxCache) Push(types.TxKey) bool   { return true }
func (NopTxCache) Remove(types.TxKey)      {}
func (NopTxCache) Has(types.TxKey) bool    { return false }
func (NopTxCache) HasKey(types.TxKey) bool { return false }

// cacheEntry stores both the transaction key and error code
type cacheEntry struct {
	key  types.TxKey
	code uint32
	log  string
}

// RejectedTxCache is a cache of rejected transactions. It wraps LRUTxCache
// to store the error code for a transaction that has been rejected.
type RejectedTxCache struct {
	cache *LRUTxCache
}

// NewRejectedTxCache creates a new rejected tx cache.
func NewRejectedTxCache(cacheSize int) *RejectedTxCache {
	return &RejectedTxCache{
		cache: NewLRUTxCache(cacheSize),
	}
}

// Reset resets the cache to an empty state.
func (c *RejectedTxCache) Reset() {
	c.cache.Reset()
}

// Push adds a tx key and error code to the cache.
func (c *RejectedTxCache) Push(key types.TxKey, code uint32, log string) bool {
	c.cache.mtx.Lock()
	defer c.cache.mtx.Unlock()

	moved, ok := c.cache.cacheMap[key]
	if ok {
		c.cache.list.MoveToBack(moved)
		return false
	}

	if c.cache.list.Len() >= c.cache.size {
		front := c.cache.list.Front()
		if front != nil {
			frontKey := front.Value.(cacheEntry).key
			delete(c.cache.cacheMap, frontKey)
			c.cache.list.Remove(front)
		}
	}

	e := c.cache.list.PushBack(cacheEntry{key: key, code: code, log: log})
	c.cache.cacheMap[key] = e

	return true
}

// Get returns the error code for a tx key if it exists in the cache.
func (c *RejectedTxCache) Get(key types.TxKey) (uint32, string, bool) {
	c.cache.mtx.Lock()
	defer c.cache.mtx.Unlock()

	entry, ok := c.cache.cacheMap[key]
	if !ok {
		return 0, "", false
	}
	if cacheEntry, ok := entry.Value.(cacheEntry); ok {
		return cacheEntry.code, cacheEntry.log, true
	}
	return 0, "", false
}

// Has returns true if the tx key is present in the cache.
func (c *RejectedTxCache) Has(key types.TxKey) bool {
	return c.cache.Has(key)
}

// Remove removes a tx from the cache.
func (c *RejectedTxCache) Remove(txKey types.TxKey) {
	c.cache.Remove(txKey)
}
