package cat

import (
	"container/list"
	"time"

	tmsync "github.com/cometbft/cometbft/libs/sync"
	"github.com/cometbft/cometbft/types"
)

// LRUTxCache maintains a thread-safe LRU cache of raw transactions. The cache
// only stores the hash of the raw transaction.
// NOTE: This has been copied from mempool/cache with the main difference of using
// tx keys instead of raw transactions.
type LRUTxCache struct {
	staticSize int

	mtx tmsync.Mutex
	// cacheMap is used as a quick look-up table
	cacheMap map[types.TxKey]*list.Element
	// list is a doubly linked list used to capture the FIFO nature of the cache
	list *list.List
}

func NewLRUTxCache(cacheSize int) *LRUTxCache {
	return &LRUTxCache{
		staticSize: cacheSize,
		cacheMap:   make(map[types.TxKey]*list.Element, cacheSize),
		list:       list.New(),
	}
}

func (c *LRUTxCache) Reset() {
	c.mtx.Lock()
	defer c.mtx.Unlock()

	c.cacheMap = make(map[types.TxKey]*list.Element, c.staticSize)
	c.list.Init()
}

func (c *LRUTxCache) Push(txKey types.TxKey) bool {
	if c.staticSize == 0 {
		return true
	}

	c.mtx.Lock()
	defer c.mtx.Unlock()

	moved, ok := c.cacheMap[txKey]
	if ok {
		c.list.MoveToBack(moved)
		return false
	}

	if c.list.Len() >= c.staticSize {
		front := c.list.Front()
		if front != nil {
			frontKey := front.Value.(types.TxKey)
			delete(c.cacheMap, frontKey)
			c.list.Remove(front)
		}
	}

	e := c.list.PushBack(txKey)
	c.cacheMap[txKey] = e

	return true
}

func (c *LRUTxCache) Remove(txKey types.TxKey) {
	if c.staticSize == 0 {
		return
	}

	c.mtx.Lock()
	defer c.mtx.Unlock()

	e := c.cacheMap[txKey]
	delete(c.cacheMap, txKey)

	if e != nil {
		c.list.Remove(e)
	}
}

func (c *LRUTxCache) Has(txKey types.TxKey) bool {
	if c.staticSize == 0 {
		return false
	}

	c.mtx.Lock()
	defer c.mtx.Unlock()

	_, ok := c.cacheMap[txKey]
	return ok
}

// cacheEntry stores both the transaction key and error code
type cacheEntry struct {
	key  types.TxKey
	code uint32
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
func (c *RejectedTxCache) Push(key types.TxKey, code uint32) bool {
	c.cache.mtx.Lock()
	defer c.cache.mtx.Unlock()

	moved, ok := c.cache.cacheMap[key]
	if ok {
		c.cache.list.MoveToBack(moved)
		return false
	}

	if c.cache.list.Len() >= c.cache.staticSize {
		front := c.cache.list.Front()
		if front != nil {
			frontKey := front.Value.(cacheEntry).key
			delete(c.cache.cacheMap, frontKey)
			c.cache.list.Remove(front)
		}
	}

	e := c.cache.list.PushBack(cacheEntry{key: key, code: code})
	c.cache.cacheMap[key] = e

	return true
}

// Get returns the error code for a tx key if it exists in the cache.
func (c *RejectedTxCache) Get(key types.TxKey) (uint32, bool) {
	c.cache.mtx.Lock()
	defer c.cache.mtx.Unlock()

	entry, ok := c.cache.cacheMap[key]
	if !ok {
		return 0, false
	}
	if cacheEntry, ok := entry.Value.(cacheEntry); ok {
		return cacheEntry.code, true
	}
	return 0, false
}

// Has returns true if the tx key is present in the cache.
func (c *RejectedTxCache) Has(key types.TxKey) bool {
	return c.cache.Has(key)
}

// Remove removes a tx from the cache.
func (c *RejectedTxCache) Remove(txKey types.TxKey) {
	c.cache.Remove(txKey)
}

// SeenTxSet records transactions that have been
// seen by other peers but not yet by us
type SeenTxSet struct {
	mtx tmsync.Mutex
	set map[types.TxKey]timestampedPeerSet
}

type timestampedPeerSet struct {
	peers map[uint16]struct{}
	time  time.Time
}

func NewSeenTxSet() *SeenTxSet {
	return &SeenTxSet{
		set: make(map[types.TxKey]timestampedPeerSet),
	}
}

func (s *SeenTxSet) Add(txKey types.TxKey, peer uint16) {
	if peer == 0 {
		return
	}
	s.mtx.Lock()
	defer s.mtx.Unlock()
	seenSet, exists := s.set[txKey]
	if !exists {
		s.set[txKey] = timestampedPeerSet{
			peers: map[uint16]struct{}{peer: {}},
			time:  time.Now().UTC(),
		}
	} else {
		seenSet.peers[peer] = struct{}{}
	}
}

func (s *SeenTxSet) RemoveKey(txKey types.TxKey) {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	delete(s.set, txKey)
}

func (s *SeenTxSet) Remove(txKey types.TxKey, peer uint16) {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	set, exists := s.set[txKey]
	if exists {
		if len(set.peers) == 1 {
			delete(s.set, txKey)
		} else {
			delete(set.peers, peer)
		}
	}
}

func (s *SeenTxSet) RemovePeer(peer uint16) {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	for key, seenSet := range s.set {
		delete(seenSet.peers, peer)
		if len(seenSet.peers) == 0 {
			delete(s.set, key)
		}
	}
}

func (s *SeenTxSet) Prune(limit time.Time) {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	for key, seenSet := range s.set {
		if seenSet.time.Before(limit) {
			delete(s.set, key)
		}
	}
}

func (s *SeenTxSet) Has(txKey types.TxKey, peer uint16) bool {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	seenSet, exists := s.set[txKey]
	if !exists {
		return false
	}
	_, has := seenSet.peers[peer]
	return has
}

func (s *SeenTxSet) Get(txKey types.TxKey) map[uint16]struct{} {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	seenSet, exists := s.set[txKey]
	if !exists {
		return nil
	}
	// make a copy of the struct to avoid concurrency issues
	peers := make(map[uint16]struct{}, len(seenSet.peers))
	for peer := range seenSet.peers {
		peers[peer] = struct{}{}
	}
	return peers
}

// Len returns the amount of cached items. Mostly used for testing.
func (s *SeenTxSet) Len() int {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	return len(s.set)
}

func (s *SeenTxSet) Reset() {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	s.set = make(map[types.TxKey]timestampedPeerSet)
}
