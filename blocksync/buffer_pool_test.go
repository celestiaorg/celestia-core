package blocksync

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestByteBufferPool_GetPut(t *testing.T) {
	pool := NewByteBufferPool()

	// Get a buffer from the pool for an 80MB block
	requestedSize := 80 * 1024 * 1024 // 80MB
	buf1 := pool.Get(requestedSize)
	require.NotNil(t, buf1)
	assert.Equal(t, 0, buf1.Len())
	// Should be rounded up to 128MB (next power of 2)
	expectedSize := 128 * 1024 * 1024
	assert.Equal(t, expectedSize, buf1.Cap())

	// Write some data
	testData := []byte("test data")
	n, err := buf1.Write(testData)
	require.NoError(t, err)
	assert.Equal(t, len(testData), n)
	assert.Equal(t, len(testData), buf1.Len())

	// Return to pool
	pool.Put(buf1)

	// Get another buffer with same size - should get the same one from pool
	buf2 := pool.Get(requestedSize)
	require.NotNil(t, buf2)
	// Buffer should be reset
	assert.Equal(t, 0, buf2.Len())
	// Should have same capacity (from pool)
	assert.Equal(t, expectedSize, buf2.Cap())
}

func TestByteBufferPool_LargeBuffer(t *testing.T) {
	pool := NewByteBufferPool()

	// Request a buffer larger than maxByteBufferSize
	buf := pool.Get(maxByteBufferSize + 1024*1024)
	require.NotNil(t, buf)

	// Should be capped at maxByteBufferSize
	assert.Equal(t, maxByteBufferSize, buf.Cap())

	// Write large data that causes buffer to grow beyond max
	largeData := make([]byte, maxByteBufferSize+1024*1024)
	buf.Write(largeData)

	assert.Greater(t, buf.Cap(), maxByteBufferSize)

	// When we put it back, it should be discarded (not returned to pool)
	pool.Put(buf)

	// Get a new buffer with normal size
	buf2 := pool.Get(80 * 1024 * 1024) // 80MB
	require.NotNil(t, buf2)
	assert.Equal(t, 0, buf2.Len())
	// Should have 128MB capacity (next power of 2), not the oversized one
	assert.Equal(t, 128*1024*1024, buf2.Cap())
}

func TestByteBuffer_Operations(t *testing.T) {
	buf := &ByteBuffer{
		buf: make([]byte, 0, 1024),
	}

	// Test Write
	data := []byte("hello")
	n, err := buf.Write(data)
	require.NoError(t, err)
	assert.Equal(t, len(data), n)
	assert.Equal(t, data, buf.Bytes())

	// Test Write again (append)
	data2 := []byte(" world")
	n, err = buf.Write(data2)
	require.NoError(t, err)
	assert.Equal(t, len(data2), n)
	assert.Equal(t, []byte("hello world"), buf.Bytes())

	// Test Reset
	buf.Reset()
	assert.Equal(t, 0, buf.Len())
	assert.Greater(t, buf.Cap(), 0)

	// Test Grow
	buf.Grow(2048)
	assert.GreaterOrEqual(t, buf.Cap(), 2048)
}

func TestByteBufferPool_Concurrent(t *testing.T) {
	pool := NewByteBufferPool()
	const goroutines = 10
	const iterations = 100

	done := make(chan bool, goroutines)

	for i := 0; i < goroutines; i++ {
		go func() {
			for j := 0; j < iterations; j++ {
				// Request different sizes to test multiple pools
				size := (j%3 + 1) * 32 * 1024 * 1024 // 32MB, 64MB, or 96MB
				buf := pool.Get(size)
				buf.Write([]byte("test"))
				pool.Put(buf)
			}
			done <- true
		}()
	}

	for i := 0; i < goroutines; i++ {
		<-done
	}
}

func BenchmarkByteBufferPool_GetPut(b *testing.B) {
	pool := NewByteBufferPool()
	size := 80 * 1024 * 1024 // 80MB

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf := pool.Get(size)
		buf.Write(make([]byte, 1024))
		pool.Put(buf)
	}
}

func BenchmarkByteBufferPool_NoPool(b *testing.B) {
	size := 128 * 1024 * 1024 // 128MB (next power of 2 from 80MB)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf := make([]byte, 0, size)
		buf = append(buf, make([]byte, 1024)...)
		_ = buf
	}
}

// BenchmarkByteBufferPool_RealWorld simulates real blocksync usage:
// receiving blocks of varying sizes in sequence (not all from pool)
func BenchmarkByteBufferPool_RealWorld_WithPool(b *testing.B) {
	pool := NewByteBufferPool()
	// Varying block sizes (in MB)
	sizes := []int{
		80 * 1024 * 1024,
		100 * 1024 * 1024,
		50 * 1024 * 1024,
		120 * 1024 * 1024,
		32 * 1024 * 1024,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		size := sizes[i%len(sizes)]
		buf := pool.Get(size)
		buf.Write(make([]byte, 1024)) // Simulate writing block data
		pool.Put(buf)
	}
}

func BenchmarkByteBufferPool_RealWorld_NoPool(b *testing.B) {
	// Varying block sizes rounded to next power of 2
	sizes := []int{
		128 * 1024 * 1024, // 80MB rounded
		128 * 1024 * 1024, // 100MB rounded
		64 * 1024 * 1024,  // 50MB rounded
		128 * 1024 * 1024, // 120MB rounded
		32 * 1024 * 1024,  // 32MB exact
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		size := sizes[i%len(sizes)]
		buf := make([]byte, 0, size)
		buf = append(buf, make([]byte, 1024)...)
		_ = buf
	}
}

func TestByteBufferPool_PowerOf2Rounding(t *testing.T) {
	pool := NewByteBufferPool()

	testCases := []struct {
		requestedSize int
		expectedSize  int
		description   string
	}{
		{80 * 1024 * 1024, 128 * 1024 * 1024, "80MB → 128MB"},
		{100 * 1024 * 1024, 128 * 1024 * 1024, "100MB → 128MB"},
		{128 * 1024 * 1024, 128 * 1024 * 1024, "128MB → 128MB (exact)"},
		{150 * 1024 * 1024, 256 * 1024 * 1024, "150MB → 256MB"},
		{32 * 1024 * 1024, 32 * 1024 * 1024, "32MB → 32MB (exact)"},
		{50 * 1024 * 1024, 64 * 1024 * 1024, "50MB → 64MB"},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			buf := pool.Get(tc.requestedSize)
			assert.Equal(t, tc.expectedSize, buf.Cap(), tc.description)
			pool.Put(buf)
		})
	}
}

func TestByteBufferPool_MultipleSizeClasses(t *testing.T) {
	pool := NewByteBufferPool()

	// Get buffers of different sizes
	buf32 := pool.Get(32 * 1024 * 1024)   // 32MB
	buf64 := pool.Get(64 * 1024 * 1024)   // 64MB
	buf128 := pool.Get(128 * 1024 * 1024) // 128MB

	assert.Equal(t, 32*1024*1024, buf32.Cap())
	assert.Equal(t, 64*1024*1024, buf64.Cap())
	assert.Equal(t, 128*1024*1024, buf128.Cap())

	// Return them to pool
	pool.Put(buf32)
	pool.Put(buf64)
	pool.Put(buf128)

	// Get them again - should get from respective pools
	buf32_2 := pool.Get(32 * 1024 * 1024)
	buf64_2 := pool.Get(64 * 1024 * 1024)
	buf128_2 := pool.Get(128 * 1024 * 1024)

	assert.Equal(t, 32*1024*1024, buf32_2.Cap())
	assert.Equal(t, 64*1024*1024, buf64_2.Cap())
	assert.Equal(t, 128*1024*1024, buf128_2.Cap())
}
