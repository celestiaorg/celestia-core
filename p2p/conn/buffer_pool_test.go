package conn

import (
	"testing"

	"github.com/cometbft/cometbft/libs/trace"
	"github.com/stretchr/testify/require"
)

func TestPowerOf2BufferPool(t *testing.T) {
	pool := NewPowerOf2BufferPool(trace.NoOpTracer(), 0)

	testCases := []struct {
		name           string
		requestedSize  int
		expectedCap    int
		expectedBucket int
	}{
		{
			name:           "Small buffer rounds to minBufferSize",
			requestedSize:  100,
			expectedCap:    1024, // minBufferSize = 1KB
			expectedBucket: 10,   // 2^10 = 1024
		},
		{
			name:           "1MB buffer rounds to 1MB",
			requestedSize:  1024 * 1024,
			expectedCap:    1024 * 1024,
			expectedBucket: 20, // 2^20 = 1MB
		},
		{
			name:           "80MB buffer rounds to 128MB",
			requestedSize:  80 * 1024 * 1024,
			expectedCap:    128 * 1024 * 1024, // 2^27
			expectedBucket: 27,
		},
		{
			name:           "100MB buffer rounds to 128MB",
			requestedSize:  100 * 1024 * 1024,
			expectedCap:    128 * 1024 * 1024,
			expectedBucket: 27,
		},
		{
			name:           "150MB buffer rounds to 256MB",
			requestedSize:  150 * 1024 * 1024,
			expectedCap:    256 * 1024 * 1024, // 2^28
			expectedBucket: 28,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			buf := pool.Get(tc.requestedSize)
			require.NotNil(t, buf)
			require.Equal(t, 0, len(buf), "buffer should have zero length")
			require.Equal(t, tc.expectedCap, cap(buf), "buffer capacity should be rounded to power of 2")

			// Return to pool
			pool.Put(buf)
		})
	}
}

func TestPowerOf2BufferPoolReuse(t *testing.T) {
	pool := NewPowerOf2BufferPool(trace.NoOpTracer(), 0)

	// Get a buffer
	buf1 := pool.Get(80 * 1024 * 1024)
	require.Equal(t, 128*1024*1024, cap(buf1))

	// Write some data to it
	buf1 = append(buf1, []byte("test data")...)
	require.Equal(t, 9, len(buf1))

	// Return to pool
	pool.Put(buf1)

	// Get another buffer of similar size - should reuse from same bucket
	buf2 := pool.Get(100 * 1024 * 1024)
	require.Equal(t, 128*1024*1024, cap(buf2), "should get buffer from same 128MB bucket")
	require.Equal(t, 0, len(buf2), "buffer should be reset to zero length")
}

func TestPowerOf2BufferPoolMaxSize(t *testing.T) {
	pool := NewPowerOf2BufferPool(trace.NoOpTracer(), 0)

	// Request buffer larger than maxBufferSize - gets capped at 256MB
	buf := pool.Get(300 * 1024 * 1024)
	require.Equal(t, 256*1024*1024, cap(buf), "should cap at maxBufferSize")

	// Grow buffer beyond maxBufferSize
	largeBuf := make([]byte, 0, 300*1024*1024)
	largeBuf = append(largeBuf, make([]byte, 300*1024*1024)...)

	// Return oversized buffer - should be discarded (not counted in puts)
	pool.Put(largeBuf)

	// Stats should show the buffer was discarded
	gets, puts := pool.Stats()
	require.Equal(t, int64(1), gets, "should have 1 get")
	require.Equal(t, int64(0), puts, "should have 0 puts (oversized was discarded)")

	// Return the normal buffer
	pool.Put(buf)

	gets, puts = pool.Stats()
	require.Equal(t, int64(1), gets, "should still have 1 get")
	require.Equal(t, int64(1), puts, "should now have 1 put")
}

func TestPowerOf2BufferPoolMultipleBuckets(t *testing.T) {
	pool := NewPowerOf2BufferPool(trace.NoOpTracer(), 0)

	// Get buffers of different sizes to create multiple buckets
	buf1KB := pool.Get(1024)
	buf1MB := pool.Get(1024 * 1024)
	buf128MB := pool.Get(128 * 1024 * 1024)

	require.Equal(t, 1024, cap(buf1KB))
	require.Equal(t, 1024*1024, cap(buf1MB))
	require.Equal(t, 128*1024*1024, cap(buf128MB))

	// Return all to pool
	pool.Put(buf1KB)
	pool.Put(buf1MB)
	pool.Put(buf128MB)

	// Get again - should reuse from correct buckets
	buf1KB_2 := pool.Get(500)   // Should get from 1KB bucket
	buf1MB_2 := pool.Get(800000) // Should get from 1MB bucket
	buf128MB_2 := pool.Get(100 * 1024 * 1024) // Should get from 128MB bucket

	require.Equal(t, 1024, cap(buf1KB_2))
	require.Equal(t, 1024*1024, cap(buf1MB_2))
	require.Equal(t, 128*1024*1024, cap(buf128MB_2))

	// Should have 6 gets and 3 puts
	gets, puts := pool.Stats()
	require.Equal(t, int64(6), gets)
	require.Equal(t, int64(3), puts)

	// Leak count should be 3 (we didn't return the second batch)
	require.Equal(t, int64(3), pool.LeakCount())
}

func TestPowerOf2BufferPoolLeakDetection(t *testing.T) {
	pool := NewPowerOf2BufferPool(trace.NoOpTracer(), 0)

	// Get 5 buffers
	for i := 0; i < 5; i++ {
		pool.Get(1024 * 1024)
	}

	// Return only 2
	buf1 := pool.Get(1024 * 1024)
	buf2 := pool.Get(1024 * 1024)
	pool.Put(buf1)
	pool.Put(buf2)

	gets, puts := pool.Stats()
	require.Equal(t, int64(7), gets, "should have 7 gets")
	require.Equal(t, int64(2), puts, "should have 2 puts")
	require.Equal(t, int64(5), pool.LeakCount(), "should detect 5 leaked buffers")
}

func TestNextPowerOf2(t *testing.T) {
	testCases := []struct {
		input    int
		expected int
	}{
		{0, 1},
		{1, 1},
		{2, 2},
		{3, 4},
		{4, 4},
		{5, 8},
		{1023, 1024},
		{1024, 1024},
		{1025, 2048},
		{80 * 1024 * 1024, 128 * 1024 * 1024},
		{128 * 1024 * 1024, 128 * 1024 * 1024},
		{129 * 1024 * 1024, 256 * 1024 * 1024},
	}

	for _, tc := range testCases {
		result := nextPowerOf2(tc.input)
		require.Equal(t, tc.expected, result, "nextPowerOf2(%d) should be %d", tc.input, tc.expected)
	}
}
