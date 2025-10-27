package conn

import (
	"sync"
	"sync/atomic"
)

// BufferPool is an interface for getting and returning byte buffers.
// This allows different pooling strategies (sync.Pool, fixed pool, etc.)
type BufferPool interface {
	// Get retrieves a buffer from the pool with at least minCap capacity
	Get(minCap int) []byte

	// Put returns a buffer to the pool
	Put(buf []byte)
}

// SyncBufferPool implements BufferPool using sync.Pool with leak detection.
type SyncBufferPool struct {
	pool       sync.Pool
	defaultCap int
	maxCap     int

	// Leak detection - tracks Get/Put calls
	getCount atomic.Int64
	putCount atomic.Int64
}

// NewSyncBufferPool creates a new buffer pool with the specified default and max capacities.
// defaultCap: the initial capacity of buffers created by the pool (e.g., 80MB for Celestia blocks)
// maxCap: buffers larger than this will be discarded instead of returned to the pool (e.g., 160MB)
func NewSyncBufferPool(defaultCap, maxCap int) *SyncBufferPool {
	return &SyncBufferPool{
		pool: sync.Pool{
			New: func() interface{} {
				return make([]byte, 0, defaultCap)
			},
		},
		defaultCap: defaultCap,
		maxCap:     maxCap,
	}
}

// Get retrieves a buffer from the pool with at least minCap capacity.
// If the pooled buffer is too small, a new larger buffer is allocated.
func (p *SyncBufferPool) Get(minCap int) []byte {
	p.getCount.Add(1)
	buf := p.pool.Get().([]byte)

	// If buffer is too small, grow it
	if cap(buf) < minCap {
		buf = make([]byte, 0, minCap)
	}

	return buf
}

// Put returns a buffer to the pool.
// Buffers larger than maxCap are discarded to prevent the pool from
// holding onto extremely large buffers.
func (p *SyncBufferPool) Put(buf []byte) {
	// Don't return oversized buffers to pool
	if cap(buf) > p.maxCap {
		return
	}

	p.putCount.Add(1)

	// Reset length but keep capacity
	buf = buf[:0]
	p.pool.Put(buf)
}

// Stats returns the number of Get and Put calls made to the pool.
// Useful for monitoring and leak detection.
func (p *SyncBufferPool) Stats() (gets, puts int64) {
	return p.getCount.Load(), p.putCount.Load()
}

// LeakCount returns the number of buffers that have been retrieved
// but not yet returned to the pool.
// A growing leak count indicates buffers are not being returned properly.
func (p *SyncBufferPool) LeakCount() int64 {
	return p.getCount.Load() - p.putCount.Load()
}
