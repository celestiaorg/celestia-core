package blocksync_test

import (
	"fmt"

	"github.com/cometbft/cometbft/blocksync"
)

// ExampleByteBufferPool demonstrates size-based buffer pooling with power-of-2 rounding.
func ExampleByteBufferPool() {
	pool := blocksync.NewByteBufferPool()

	// Request buffer for an 80MB block
	// This will be rounded up to 128MB (next power of 2)
	buf := pool.Get(80 * 1024 * 1024)
	fmt.Printf("Requested: 80MB, Got capacity: %dMB\n", buf.Cap()/(1024*1024))

	// Write some data
	buf.Write([]byte("block data"))
	fmt.Printf("Length after write: %d bytes\n", buf.Len())

	// Return to pool (buffer will be reset but capacity preserved)
	pool.Put(buf)

	// Get another buffer of same size - gets from same pool
	buf2 := pool.Get(80 * 1024 * 1024)
	fmt.Printf("Buffer from pool, length: %d, capacity: %dMB\n",
		buf2.Len(), buf2.Cap()/(1024*1024))

	// Different block size - uses different pool
	largeBuf := pool.Get(150 * 1024 * 1024)
	fmt.Printf("Requested: 150MB, Got capacity: %dMB\n", largeBuf.Cap()/(1024*1024))

	// Output:
	// Requested: 80MB, Got capacity: 128MB
	// Length after write: 10 bytes
	// Buffer from pool, length: 0, capacity: 128MB
	// Requested: 150MB, Got capacity: 256MB
}

// Example showing how different sizes map to power-of-2 pools.
func ExampleByteBufferPool_powerOf2Rounding() {
	pool := blocksync.NewByteBufferPool()

	sizes := []int{
		32 * 1024 * 1024,  // 32MB
		50 * 1024 * 1024,  // 50MB
		80 * 1024 * 1024,  // 80MB
		100 * 1024 * 1024, // 100MB
		150 * 1024 * 1024, // 150MB
	}

	for _, size := range sizes {
		buf := pool.Get(size)
		fmt.Printf("%dMB → %dMB\n", size/(1024*1024), buf.Cap()/(1024*1024))
		pool.Put(buf)
	}

	// Output:
	// 32MB → 32MB
	// 50MB → 64MB
	// 80MB → 128MB
	// 100MB → 128MB
	// 150MB → 256MB
}
