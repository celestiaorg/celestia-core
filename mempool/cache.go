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
<<<<<<< HEAD
	Push(tx *types.CachedTx) bool

	// Remove removes the given raw transaction from the cache.
	Remove(tx *types.CachedTx)
=======
	Push(key types.TxKey) bool

	// Remove removes the given raw transaction from the cache.
	Remove(key types.TxKey)
>>>>>>> cb865217 (feat: simplify caching and expose rejected txs in TxStatus (#1838))

	// Has reports whether tx key is present in the cache. Checking for presence is
	// not treated as an access of the value.
<<<<<<< HEAD
	Has(tx *types.CachedTx) bool

	// HasKey reports whether the given key is present in the cache.
	HasKey(key types.TxKey) bool
=======
	Has(key types.TxKey) bool
>>>>>>> cb865217 (feat: simplify caching and expose rejected txs in TxStatus (#1838))
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

<<<<<<< HEAD
func (c *LRUTxCache) Push(tx *types.CachedTx) bool {
=======
func (c *LRUTxCache) Push(key types.TxKey) bool {
>>>>>>> cb865217 (feat: simplify caching and expose rejected txs in TxStatus (#1838))
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

<<<<<<< HEAD
func (c *LRUTxCache) Remove(tx *types.CachedTx) {
	key := tx.Key()
	c.RemoveTxByKey(key)
}

func (c *LRUTxCache) RemoveTxByKey(key types.TxKey) {
=======
func (c *LRUTxCache) Remove(key types.TxKey) {
>>>>>>> cb865217 (feat: simplify caching and expose rejected txs in TxStatus (#1838))
	c.mtx.Lock()
	defer c.mtx.Unlock()

	e := c.cacheMap[key]
	delete(c.cacheMap, key)

	if e != nil {
		c.list.Remove(e)
	}
}

<<<<<<< HEAD
func (c *LRUTxCache) Has(tx *types.CachedTx) bool {
	c.mtx.Lock()
	defer c.mtx.Unlock()

	_, ok := c.cacheMap[tx.Key()]
	return ok
}

func (c *LRUTxCache) HasKey(key types.TxKey) bool {
=======
func (c *LRUTxCache) Has(key types.TxKey) bool {
>>>>>>> cb865217 (feat: simplify caching and expose rejected txs in TxStatus (#1838))
	c.mtx.Lock()
	defer c.mtx.Unlock()

	_, ok := c.cacheMap[key]
	return ok
}

// NopTxCache defines a no-op raw transaction cache.
type NopTxCache struct{}

var _ TxCache = (*NopTxCache)(nil)

<<<<<<< HEAD
func (NopTxCache) Reset()                    {}
func (NopTxCache) Push(*types.CachedTx) bool { return true }
func (NopTxCache) Remove(*types.CachedTx)    {}
func (NopTxCache) Has(*types.CachedTx) bool  { return false }
func (NopTxCache) HasKey(types.TxKey) bool   { return false }
=======
func (NopTxCache) Reset()                {}
func (NopTxCache) Push(types.TxKey) bool { return true }
func (NopTxCache) Remove(types.TxKey)    {}
func (NopTxCache) Has(types.TxKey) bool  { return false }
>>>>>>> cb865217 (feat: simplify caching and expose rejected txs in TxStatus (#1838))
