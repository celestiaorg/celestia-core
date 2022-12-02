package cat

import (
	"container/list"
	"time"

	tmsync "github.com/tendermint/tendermint/libs/sync"
	"github.com/tendermint/tendermint/types"
)

// LRUTxCache maintains a thread-safe LRU cache of raw transactions. The cache
// only stores the hash of the raw transaction.
type LRUTxCache struct {
	mtx      tmsync.Mutex
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

func (c *LRUTxCache) Push(txKey types.TxKey) bool {
	if c.size == 0 {
		return true
	}

	c.mtx.Lock()
	defer c.mtx.Unlock()

	moved, ok := c.cacheMap[txKey]
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

	e := c.list.PushBack(txKey)
	c.cacheMap[txKey] = e

	return true
}

func (c *LRUTxCache) Remove(txKey types.TxKey) {
	c.mtx.Lock()
	defer c.mtx.Unlock()

	e := c.cacheMap[txKey]
	delete(c.cacheMap, txKey)

	if e != nil {
		c.list.Remove(e)
	}
}

func (c *LRUTxCache) Has(txKey types.TxKey) bool {
	if c.size == 0 {
		return false
	}

	c.mtx.Lock()
	defer c.mtx.Unlock()

	_, ok := c.cacheMap[txKey]
	return ok
}

type EvictedTxInfo struct {
	timeEvicted time.Time
	priority    int64
	gasWanted   int64
	sender      string
	peers       map[uint16]bool
}

type EvictedTxCache struct {
	mtx   tmsync.Mutex
	size  int
	cache map[types.TxKey]*EvictedTxInfo
}

func NewEvictedTxCache(size int) *EvictedTxCache {
	return &EvictedTxCache{
		size:  size,
		cache: make(map[types.TxKey]*EvictedTxInfo),
	}
}

func (c *EvictedTxCache) Has(txKey types.TxKey) bool {
	c.mtx.Lock()
	defer c.mtx.Unlock()
	_, exists := c.cache[txKey]
	return exists
}

func (c *EvictedTxCache) Push(wtx *WrappedTx) {
	c.mtx.Lock()
	defer c.mtx.Unlock()
	c.cache[wtx.key] = &EvictedTxInfo{
		timeEvicted: time.Now().UTC(),
		priority:    wtx.priority,
		gasWanted:   wtx.gasWanted,
		sender:      wtx.sender,
		peers:       wtx.peers,
	}
	// if cache too large, remove the oldest entry
	if len(c.cache) > c.size {
		oldestTxKey := wtx.key
		oldestTxTime := time.Now().UTC()
		for key, info := range c.cache {
			if info.timeEvicted.Before(oldestTxTime) {
				oldestTxTime = info.timeEvicted
				oldestTxKey = key
			}
		}
		delete(c.cache, oldestTxKey)
	}
}

func (c *EvictedTxCache) Pop(txKey types.TxKey) *EvictedTxInfo {
	c.mtx.Lock()
	defer c.mtx.Unlock()
	info, exists := c.cache[txKey]
	if !exists {
		return nil
	} else {
		delete(c.cache, txKey)
		return info
	}
}

func (c *EvictedTxCache) Prune(limit time.Time) {
	c.mtx.Lock()
	defer c.mtx.Unlock()
	for key, info := range c.cache {
		if info.timeEvicted.Before(limit) {
			delete(c.cache, key)
		}
	}
}

// seenTxSet records transactions that have been
// seen by other peers but not yet by us
type SeenTxSet struct {
	mtx  tmsync.Mutex
	size int
	set  map[types.TxKey]timestampedPeerSet
}

type timestampedPeerSet struct {
	peers map[uint16]bool
	time  time.Time
}

func NewSeenTxSet(size int) *SeenTxSet {
	return &SeenTxSet{
		size: size,
		set:  make(map[types.TxKey]timestampedPeerSet),
	}
}

func (s *SeenTxSet) Add(txKey types.TxKey, peer uint16) {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	seenSet, exists := s.set[txKey]
	if !exists {
		s.set[txKey] = timestampedPeerSet{
			peers: map[uint16]bool{peer: true},
			time:  time.Now().UTC(),
		}
		s.constrainSize()
	} else {
		seenSet.peers[peer] = true
	}
}

func (s *SeenTxSet) constrainSize() {
	if len(s.set) > s.size {
		var (
			oldestTxKey types.TxKey
			oldestTime  time.Time
		)
		for key, set := range s.set {
			if oldestTime.IsZero() || set.time.Before(oldestTime) {
				oldestTxKey = key
				oldestTime = set.time
			}
		}
		delete(s.set, oldestTxKey)
	}
}

func (s *SeenTxSet) Pop(txKey types.TxKey) map[uint16]bool {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	seenSet, exists := s.set[txKey]
	if !exists {
		return nil
	} else {
		delete(s.set, txKey)
		return seenSet.peers
	}
}

// Len returns the amount of cached items. Mostly used for testing.
func (s *SeenTxSet) Len() int {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	return len(s.set)
}
