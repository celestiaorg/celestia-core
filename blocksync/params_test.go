package blocksync

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// almostEqualRelative checks if two float64 values are approximately equal
// within a relative epsilon tolerance. Returns true if the absolute difference
// is less than or equal to epsilon times the larger of the two values.
func almostEqualRelative(a, b, epsilon float64) bool {
	diff := math.Abs(a - b)
	absA := math.Abs(a)
	absB := math.Abs(b)
	largest := math.Max(absA, absB)

	return diff <= largest*epsilon
}

func TestBlockPoolParams_DefaultValues(t *testing.T) {
	buffer := newBlockStats(blockSizeBufferCapacity)
	params := newBlockPoolParams(buffer, defaultMaxRequesters)

	// With no samples, should use default values
	assert.Equal(t, defaultMaxPendingRequestsPerPeer, params.maxPendingPerPeer)
	assert.Equal(t, time.Duration(defaultRetrySeconds*float64(time.Second)), params.retryTimeout)
	assert.Equal(t, 0, params.numSamples)
	assert.Equal(t, 0.0, params.avgBlockSize)
	assert.Equal(t, 0.0, params.maxBlockSize)
}

func TestBlockPoolParams_SmallBlocks(t *testing.T) {
	buffer := newBlockStats(blockSizeBufferCapacity)
	params := newBlockPoolParams(buffer, defaultMaxRequesters)

	// Add small blocks (1 KB)
	smallBlockSize := 1024
	for i := 0; i < minSamplesForDynamic; i++ {
		params.addBlock(smallBlockSize, 10)
	}

	// Should use max pending for small blocks
	assert.Equal(t, maxPendingForSmallBlocks, params.maxPendingPerPeer, "Small blocks should have max pending requests")
	assert.Equal(t, time.Duration(minRetrySeconds*float64(time.Second)), params.retryTimeout, "Small blocks should have min retry timeout")
	assert.Equal(t, minSamplesForDynamic, params.numSamples)
	assert.Equal(t, float64(smallBlockSize), params.avgBlockSize)
	assert.Equal(t, float64(smallBlockSize), params.maxBlockSize)
}

func TestBlockPoolParams_LargeBlocks(t *testing.T) {
	buffer := newBlockStats(blockSizeBufferCapacity)
	params := newBlockPoolParams(buffer, defaultMaxRequesters)

	// Add large blocks (20 MB+)
	largeBlockSize := 20 * 1024 * 1024
	for i := 0; i < minSamplesForDynamic; i++ {
		params.addBlock(largeBlockSize, 10)
	}

	// Should use min pending for large blocks
	assert.Equal(t, maxPendingForLargeBlocks, params.maxPendingPerPeer, "Large blocks should have min pending requests")
	assert.Equal(t, time.Duration(maxRetrySeconds*float64(time.Second)), params.retryTimeout, "Large blocks should have max retry timeout")
	assert.Equal(t, minSamplesForDynamic, params.numSamples)
	assert.Equal(t, float64(largeBlockSize), params.avgBlockSize)
	assert.Equal(t, float64(largeBlockSize), params.maxBlockSize)
}

func TestBlockPoolParams_MediumBlocks(t *testing.T) {
	buffer := newBlockStats(blockSizeBufferCapacity)
	params := newBlockPoolParams(buffer, defaultMaxRequesters)

	// Add medium blocks (5 MB - in the middle of the range)
	mediumBlockSize := 5 * 1024 * 1024
	for i := 0; i < minSamplesForDynamic; i++ {
		params.addBlock(mediumBlockSize, 10)
	}

	// Should have intermediate values
	assert.Greater(t, params.maxPendingPerPeer, maxPendingForLargeBlocks, "Medium blocks should have more pending than large blocks")
	assert.Less(t, params.maxPendingPerPeer, maxPendingForSmallBlocks, "Medium blocks should have less pending than small blocks")
	assert.Greater(t, params.retryTimeout, time.Duration(minRetrySeconds*float64(time.Second)), "Medium blocks should have longer retry than small blocks")
	assert.Less(t, params.retryTimeout, time.Duration(maxRetrySeconds*float64(time.Second)), "Medium blocks should have shorter retry than large blocks")
}

func TestBlockPoolParams_MaxPendingByBlockSize(t *testing.T) {
	tests := []struct {
		name            string
		blockSize       int
		expectedPending int
	}{
		{
			name:            "1KB blocks",
			blockSize:       1 * 1024,
			expectedPending: 40,
		},
		{
			name:            "100KB blocks",
			blockSize:       100 * 1024,
			expectedPending: 40,
		},
		{
			name:            "500KB blocks",
			blockSize:       500 * 1024,
			expectedPending: 40,
		},
		{
			name:            "1MB blocks",
			blockSize:       1 * 1024 * 1024,
			expectedPending: 40,
		},
		{
			name:            "5MB blocks",
			blockSize:       5 * 1024 * 1024,
			expectedPending: 30,
		},
		{
			name:            "10MB blocks",
			blockSize:       10 * 1024 * 1024,
			expectedPending: 20,
		},
		{
			name:            "20MB+ blocks",
			blockSize:       20 * 1024 * 1024,
			expectedPending: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buffer := newBlockStats(blockSizeBufferCapacity)
			params := newBlockPoolParams(buffer, defaultMaxRequesters)

			// Add enough samples to trigger dynamic calculation
			for i := 0; i < minSamplesForDynamic; i++ {
				params.addBlock(tt.blockSize, 10)
			}

			assert.Equal(t, tt.expectedPending, params.maxPendingPerPeer, "maxPendingPerPeer should be %d for %s", tt.expectedPending, tt.name)
		})
	}
}

func TestBlockPoolParams_RetryTimeoutByBlockSize(t *testing.T) {
	tests := []struct {
		name            string
		blockSize       int
		expectedSeconds float64
	}{
		{
			name:            "1KB blocks",
			blockSize:       1 * 1024,
			expectedSeconds: 5.00,
		},
		{
			name:            "100KB blocks",
			blockSize:       100 * 1024,
			expectedSeconds: 5.27,
		},
		{
			name:            "500KB blocks",
			blockSize:       500 * 1024,
			expectedSeconds: 6.34,
		},
		{
			name:            "1MB blocks",
			blockSize:       1 * 1024 * 1024,
			expectedSeconds: 7.75,
		},
		{
			name:            "5MB blocks",
			blockSize:       5 * 1024 * 1024,
			expectedSeconds: 18.75,
		},
		{
			name:            "10MB blocks",
			blockSize:       10 * 1024 * 1024,
			expectedSeconds: 32.50,
		},
		{
			name:            "15MB blocks",
			blockSize:       15 * 1024 * 1024,
			expectedSeconds: 46.25,
		},
		{
			name:            "20MB+ blocks",
			blockSize:       20 * 1024 * 1024,
			expectedSeconds: 60.00,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buffer := newBlockStats(blockSizeBufferCapacity)
			params := newBlockPoolParams(buffer, defaultMaxRequesters)

			// Add enough samples to trigger dynamic calculation
			for i := 0; i < minSamplesForDynamic; i++ {
				params.addBlock(tt.blockSize, 10)
			}

			retrySeconds := params.retryTimeout.Seconds()
			assert.True(t, almostEqualRelative(retrySeconds, tt.expectedSeconds, 0.1),
				"retry timeout should be ~%.2fs, got %.2fs", tt.expectedSeconds, retrySeconds)
		})
	}
}

func TestBlockPoolParams_RequestersLimit(t *testing.T) {
	maxRequesters := 100

	tests := []struct {
		name                string
		blockSize           int
		numPeers            int
		expectedMemoryLimit int
		expectedPeerLimit   int
		expectedActualLimit int
	}{
		{
			name:                "small blocks, few peers",
			blockSize:           1 * 1024, // 1 KB
			numPeers:            5,
			expectedMemoryLimit: 3145728, // 3GB / 1KB
			expectedPeerLimit:   5 * 40,  // 5 peers * 40 pending
			expectedActualLimit: 100,     // capped at maxRequesters
		},
		{
			name:                "medium blocks, many peers",
			blockSize:           1 * 1024 * 1024, // 1 MB
			numPeers:            20,
			expectedMemoryLimit: 3072,    // 3GB / 1MB
			expectedPeerLimit:   20 * 40, // 20 peers * 40 pending (1MB is still small)
			expectedActualLimit: 100,     // capped at maxRequesters
		},
		{
			name:                "large blocks, memory limited",
			blockSize:           100 * 1024 * 1024, // 100 MB
			numPeers:            50,
			expectedMemoryLimit: 30,     // 3GB / 100MB
			expectedPeerLimit:   50 * 2, // 50 peers * 2 pending (100MB is large)
			expectedActualLimit: 30,     // limited by memory
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buffer := newBlockStats(blockSizeBufferCapacity)
			params := newBlockPoolParams(buffer, maxRequesters)

			// Add enough samples to trigger dynamic calculation
			for i := 0; i < minSamplesForDynamic; i++ {
				params.addBlock(tt.blockSize, tt.numPeers)
			}

			assert.Equal(t, tt.expectedMemoryLimit, params.memoryBasedLimit, "memory-based limit mismatch")
			assert.Equal(t, tt.expectedPeerLimit, params.peerBasedLimit, "peer-based limit mismatch")
			assert.Equal(t, tt.expectedActualLimit, params.requestersLimit, "actual requesters limit mismatch")
		})
	}
}

func TestBlockPoolParams_MixedBlockSizes(t *testing.T) {
	buffer := newBlockStats(blockSizeBufferCapacity)
	params := newBlockPoolParams(buffer, defaultMaxRequesters)

	// Add mix of small and large blocks
	blockSizes := []int{
		10 * 1024,        // 10 KB
		50 * 1024,        // 50 KB
		100 * 1024,       // 100 KB
		1 * 1024 * 1024,  // 1 MB
		10 * 1024 * 1024, // 10 MB (largest)
	}

	for _, size := range blockSizes {
		params.addBlock(size, 10)
	}

	// Should use max block size (worst case) for calculations
	assert.Equal(t, float64(10*1024*1024), params.maxBlockSize, "Should track max block size")

	// Average should be lower than max
	assert.Less(t, params.avgBlockSize, params.maxBlockSize, "Average should be less than max")

	// Max pending should be based on the largest block (10 MB = 20 pending)
	assert.LessOrEqual(t, params.maxPendingPerPeer, 25, "Should use conservative pending based on max block size")
}

func TestBlockPoolParams_InsufficientSamples(t *testing.T) {
	buffer := newBlockStats(blockSizeBufferCapacity)
	params := newBlockPoolParams(buffer, defaultMaxRequesters)

	// Add fewer samples than required
	for i := 0; i < minSamplesForDynamic-1; i++ {
		params.addBlock(10*1024*1024, 10) // Large blocks
	}

	// Should still use defaults despite large blocks
	assert.Equal(t, defaultMaxPendingRequestsPerPeer, params.maxPendingPerPeer, "Should use default with insufficient samples")
	assert.Equal(t, time.Duration(defaultRetrySeconds*float64(time.Second)), params.retryTimeout, "Should use default retry timeout with insufficient samples")
	assert.Equal(t, minSamplesForDynamic-1, params.numSamples)
}

func TestBlockPoolParams_Recalculate(t *testing.T) {
	buffer := newBlockStats(blockSizeBufferCapacity)
	params := newBlockPoolParams(buffer, defaultMaxRequesters)

	// Add initial blocks
	for i := 0; i < minSamplesForDynamic; i++ {
		params.addBlock(1024, 2) // Small blocks, 2 peers (low peer count)
	}

	initialPending := params.maxPendingPerPeer
	initialLimit := params.requestersLimit

	// Recalculate with more peers
	params.recalculate(10)

	// maxPendingPerPeer should stay the same (based on block size)
	assert.Equal(t, initialPending, params.maxPendingPerPeer, "maxPendingPerPeer should not change")

	// requestersLimit should change (based on num peers)
	assert.NotEqual(t, initialLimit, params.requestersLimit, "requestersLimit should update with peer count")
	assert.Equal(t, 10*params.maxPendingPerPeer, params.peerBasedLimit, "peer-based limit should update")
}

func TestBlockPoolParams_CalculateMaxPending(t *testing.T) {
	buffer := newBlockStats(blockSizeBufferCapacity)
	params := newBlockPoolParams(buffer, defaultMaxRequesters)

	tests := []struct {
		blockSize       float64
		expectedPending int
	}{
		{500, 40},              // Below min
		{1024, 40},             // At min boundary
		{100 * 1024, 40},       // Small
		{500 * 1024, 40},       // Still small
		{1 * 1024 * 1024, 40},  // Still small
		{5 * 1024 * 1024, 30},  // Medium
		{10 * 1024 * 1024, 20}, // Large
		{20 * 1024 * 1024, 2},  // At max boundary
		{30 * 1024 * 1024, 2},  // Above max
	}

	for _, tt := range tests {
		result := params.calculateMaxPending(tt.blockSize)
		assert.Equal(t, tt.expectedPending, result, "Block size %.0f should have pending %d, got %d", tt.blockSize, tt.expectedPending, result)

		// Verify ladder effect (should be multiple of 5 or exactly 2)
		if result != 2 {
			assert.Equal(t, 0, result%5, "Result should be multiple of 5 for block size %.0f", tt.blockSize)
		}
	}
}

func TestBlockPoolParams_CalculateRetryTimeout(t *testing.T) {
	buffer := newBlockStats(blockSizeBufferCapacity)
	params := newBlockPoolParams(buffer, defaultMaxRequesters)

	tests := []struct {
		blockSize       float64
		expectedSeconds float64
	}{
		{500, 5.00},               // Below min
		{1024, 5.00},              // At min boundary
		{1 * 1024 * 1024, 7.75},   // Small-medium
		{5 * 1024 * 1024, 18.75},  // Medium
		{10 * 1024 * 1024, 32.50}, // Medium-large
		{20 * 1024 * 1024, 60.00}, // At max boundary
		{30 * 1024 * 1024, 60.00}, // Above max
	}

	for _, tt := range tests {
		result := params.calculateRetryTimeout(tt.blockSize)
		seconds := result.Seconds()
		assert.True(t, almostEqualRelative(seconds, tt.expectedSeconds, 0.1),
			"Block size %.0f should have retry ~%.2fs, got %.2fs", tt.blockSize, tt.expectedSeconds, seconds)
	}
}

func TestBlockPoolParams_ZeroMaxBlockSize(t *testing.T) {
	buffer := newBlockStats(blockSizeBufferCapacity)
	params := newBlockPoolParams(buffer, 50)

	// With zero max block size, memory limit should default to maxRequesters
	params.recalculate(10)

	assert.Equal(t, 50, params.memoryBasedLimit, "Memory limit should be maxRequesters when maxBlockSize is 0")
	require.Equal(t, 50, params.requestersLimit, "Should not panic with zero block size")
}
