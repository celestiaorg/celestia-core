package conn

import (
	"math/bits"
	"sync"
	"sync/atomic"

	"github.com/cometbft/cometbft/libs/trace"
	"github.com/cometbft/cometbft/libs/trace/schema"
)

const (
	// minBufferSize is the minimum buffer size (1KB).
	// Smaller allocations are cheap enough to not warrant pooling.
	minBufferSize = 1 * 1024 // 1KB

	// maxBufferSize is the maximum size we'll keep in the pool.
	// Buffers larger than this are discarded instead of returning to pool.
	// Set to 256MB to handle Celestia blocks up to 128MB with some overhead.
	maxBufferSize = 256 * 1024 * 1024 // 256MB
)

// BufferPool is an interface for getting and returning byte buffers.
// This allows different pooling strategies (sync.Pool, fixed pool, etc.)
type BufferPool interface {
	// Get retrieves a buffer from the pool with at least minCap capacity
	Get(minCap int) []byte

	// Put returns a buffer to the pool
	Put(buf []byte)
}

// PowerOf2BufferPool implements BufferPool using power-of-2 size class pooling.
// Buffers are pooled by size class (rounded to next power of 2) to reduce
// memory allocations and GC pressure during block sync.
//
// For example:
//   - 80 MB block → gets buffer from 128 MB pool (2^27 bytes)
//   - 100 MB block → gets buffer from 128 MB pool (2^27 bytes)
//   - 150 MB block → gets buffer from 256 MB pool (2^28 bytes)
type PowerOf2BufferPool struct {
	pools       map[int]*sync.Pool // Key is the power of 2 exponent (e.g., 27 for 128MB)
	mu          sync.RWMutex
	traceClient trace.Tracer
	channelID   int

	// Leak detection - tracks Get/Put calls
	getCount atomic.Int64
	putCount atomic.Int64
}

// NewPowerOf2BufferPool creates a new buffer pool with power-of-2 size class pooling.
func NewPowerOf2BufferPool(traceClient trace.Tracer, channelID int) *PowerOf2BufferPool {
	return &PowerOf2BufferPool{
		pools:       make(map[int]*sync.Pool),
		traceClient: traceClient,
		channelID:   channelID,
	}
}

// Get retrieves a buffer from the pool with at least the requested capacity.
// The buffer size is rounded up to the next power of 2 for efficient pooling.
func (p *PowerOf2BufferPool) Get(size int) []byte {
	p.getCount.Add(1)

	if size < minBufferSize {
		size = minBufferSize
	}
	if size > maxBufferSize {
		size = maxBufferSize
	}

	// Round up to next power of 2
	roundedSize := nextPowerOf2(size)
	sizeClass := bits.Len(uint(roundedSize)) - 1 // e.g., 128MB = 2^27 → class 27

	// Get or create pool for this size class
	pool := p.getPoolForSize(sizeClass, roundedSize)

	// Get buffer from pool
	buf := pool.Get().([]byte)

	// Ensure buffer has the rounded capacity
	if cap(buf) < roundedSize {
		buf = make([]byte, 0, roundedSize)
	}

	// Trace buffer retrieval
	schema.WriteP2PBufferPoolGet(p.traceClient, size, cap(buf), p.channelID)

	return buf
}

// getPoolForSize returns the pool for a given size class, creating it if needed.
func (p *PowerOf2BufferPool) getPoolForSize(sizeClass int, roundedSize int) *sync.Pool {
	// Try read lock first (fast path)
	p.mu.RLock()
	pool, exists := p.pools[sizeClass]
	p.mu.RUnlock()

	if exists {
		return pool
	}

	// Need to create pool (slow path)
	p.mu.Lock()
	defer p.mu.Unlock()

	// Check again in case another goroutine created it
	if pool, exists := p.pools[sizeClass]; exists {
		return pool
	}

	// Create new pool for this size class
	pool = &sync.Pool{
		New: func() interface{} {
			return make([]byte, 0, roundedSize)
		},
	}
	p.pools[sizeClass] = pool
	return pool
}

// nextPowerOf2 returns the next power of 2 >= n.
func nextPowerOf2(n int) int {
	if n <= 1 {
		return 1
	}
	// If already power of 2, return as is
	if n&(n-1) == 0 {
		return n
	}
	// Round up to next power of 2
	return 1 << bits.Len(uint(n))
}

// Put returns a buffer to the pool.
// The buffer's content is reset but its capacity is preserved.
// The buffer is returned to the appropriate size class pool based on its capacity.
func (p *PowerOf2BufferPool) Put(buf []byte) {
	if buf == nil {
		return
	}

	bufCap := cap(buf)
	discarded := false

	// If the buffer has grown too large, don't return it to the pool.
	// This prevents the pool from holding onto extremely large buffers.
	if bufCap > maxBufferSize {
		discarded = true
		schema.WriteP2PBufferPoolPut(p.traceClient, bufCap, p.channelID, discarded)
		return
	}

	// If the buffer is too small, don't pool it
	if bufCap < minBufferSize {
		discarded = true
		schema.WriteP2PBufferPoolPut(p.traceClient, bufCap, p.channelID, discarded)
		return
	}

	p.putCount.Add(1)

	// Determine which size class pool this buffer belongs to
	// The buffer should be a power of 2 size from Get(), but we verify
	sizeClass := bits.Len(uint(bufCap)) - 1

	// Get the pool for this size class
	p.mu.RLock()
	pool, exists := p.pools[sizeClass]
	p.mu.RUnlock()

	if !exists {
		// Pool doesn't exist yet - this shouldn't happen in normal operation
		// since Get() would have created it, but we handle it gracefully
		discarded = true
		schema.WriteP2PBufferPoolPut(p.traceClient, bufCap, p.channelID, discarded)
		return
	}

	// Reset the buffer but keep its capacity
	buf = buf[:0]
	pool.Put(buf)

	// Trace buffer return
	schema.WriteP2PBufferPoolPut(p.traceClient, bufCap, p.channelID, discarded)
}

// Stats returns the number of Get and Put calls made to the pool.
// Useful for monitoring and leak detection.
func (p *PowerOf2BufferPool) Stats() (gets, puts int64) {
	return p.getCount.Load(), p.putCount.Load()
}

// LeakCount returns the number of buffers that have been retrieved
// but not yet returned to the pool.
// A growing leak count indicates buffers are not being returned properly.
func (p *PowerOf2BufferPool) LeakCount() int64 {
	return p.getCount.Load() - p.putCount.Load()
}
