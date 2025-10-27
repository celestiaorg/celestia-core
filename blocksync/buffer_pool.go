package blocksync

import (
	"math/bits"
	"sync"
)

const (
	// minByteBufferSize is the minimum buffer size (1KB).
	// Smaller allocations are cheap enough to not warrant pooling.
	minByteBufferSize = 1 * 1024 // 1KB

	// maxByteBufferSize is the maximum size we'll keep in the pool.
	// If a buffer grows beyond this, we'll discard it instead of returning to pool.
	// Set to 256MB to handle Celestia blocks up to 128MB with some overhead.
	maxByteBufferSize = 256 * 1024 * 1024 // 256MB
)

// ByteBuffer wraps a byte slice for use in the buffer pool.
type ByteBuffer struct {
	buf []byte
}

// ByteBufferPool manages a pool of reusable byte buffers for block processing.
// Buffers are pooled by size class (rounded to next power of 2) to reduce
// memory allocations and GC pressure during block sync.
//
// For example:
//   - 80 MB block → gets buffer from 128 MB pool (2^27 bytes)
//   - 100 MB block → gets buffer from 128 MB pool (2^27 bytes)
//   - 150 MB block → gets buffer from 256 MB pool (2^28 bytes)
type ByteBufferPool struct {
	pools map[int]*sync.Pool // Key is the power of 2 exponent (e.g., 27 for 128MB)
	mu    sync.RWMutex
}

// NewByteBufferPool creates a new buffer pool with size-based pooling.
func NewByteBufferPool() *ByteBufferPool {
	return &ByteBufferPool{
		pools: make(map[int]*sync.Pool),
	}
}

// Get retrieves a buffer from the pool with at least the requested capacity.
// The buffer size is rounded up to the next power of 2 for efficient pooling.
func (p *ByteBufferPool) Get(size int) *ByteBuffer {
	if size < minByteBufferSize {
		size = minByteBufferSize
	}
	if size > maxByteBufferSize {
		size = maxByteBufferSize
	}

	// Round up to next power of 2
	roundedSize := nextPowerOf2(size)
	sizeClass := bits.Len(uint(roundedSize)) - 1 // e.g., 128MB = 2^27 → class 27

	// Get or create pool for this size class
	pool := p.getPoolForSize(sizeClass, roundedSize)

	// Get buffer from pool
	bb := pool.Get().(*ByteBuffer)

	// Ensure buffer has the rounded capacity
	if cap(bb.buf) < roundedSize {
		bb.buf = make([]byte, 0, roundedSize)
	}

	return bb
}

// getPoolForSize returns the pool for a given size class, creating it if needed.
func (p *ByteBufferPool) getPoolForSize(sizeClass int, roundedSize int) *sync.Pool {
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
			return &ByteBuffer{
				buf: make([]byte, 0, roundedSize),
			}
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
func (p *ByteBufferPool) Put(bb *ByteBuffer) {
	if bb == nil {
		return
	}

	bufCap := cap(bb.buf)

	// If the buffer has grown too large, don't return it to the pool.
	// This prevents the pool from holding onto extremely large buffers.
	if bufCap > maxByteBufferSize {
		return
	}

	// If the buffer is too small, don't pool it
	if bufCap < minByteBufferSize {
		return
	}

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
		return
	}

	// Reset the buffer but keep its capacity
	bb.buf = bb.buf[:0]
	pool.Put(bb)
}

// Bytes returns the underlying byte slice.
func (bb *ByteBuffer) Bytes() []byte {
	return bb.buf
}

// Grow ensures the buffer has at least n bytes of capacity.
// It returns the buffer for chaining.
func (bb *ByteBuffer) Grow(n int) *ByteBuffer {
	if cap(bb.buf)-len(bb.buf) < n {
		newBuf := make([]byte, len(bb.buf), len(bb.buf)+n)
		copy(newBuf, bb.buf)
		bb.buf = newBuf
	}
	return bb
}

// Write appends data to the buffer.
func (bb *ByteBuffer) Write(data []byte) (int, error) {
	bb.buf = append(bb.buf, data...)
	return len(data), nil
}

// Reset clears the buffer.
func (bb *ByteBuffer) Reset() {
	bb.buf = bb.buf[:0]
}

// Len returns the length of the buffer.
func (bb *ByteBuffer) Len() int {
	return len(bb.buf)
}

// Cap returns the capacity of the buffer.
func (bb *ByteBuffer) Cap() int {
	return cap(bb.buf)
}
