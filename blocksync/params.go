package blocksync

import (
	"time"
)

var (
	RequesterLimitHardCap = 40
	MaxPendingHardCap     = 2
)

// BlockPoolParams holds dynamically calculated parameters for the block pool
type BlockPoolParams struct {
	maxPendingPerPeer int
	retryTimeout      time.Duration
	requestersLimit   int
	blockSizeBuffer   *blockStats
	maxRequesters     int

	// Cached calculated values for logging
	avgBlockSize     float64 // kept for logging purposes
	maxBlockSize     float64 // used for calculations
	peerBasedLimit   int
	memoryBasedLimit int
	numSamples       int
}

// newBlockPoolParams creates a new BlockPoolParams with the given configuration
func newBlockPoolParams(blockSizeBuffer *blockStats, maxRequesters int) *BlockPoolParams {
	params := &BlockPoolParams{
		blockSizeBuffer: blockSizeBuffer,
		maxRequesters:   maxRequesters,
	}
	params.recalculate(0)
	return params
}

// recalculate updates all parameters based on current conditions
// numPeers is passed in since it's external state from BlockPool
func (p *BlockPoolParams) recalculate(numPeers int) {
	p.avgBlockSize = p.blockSizeBuffer.GetAverage()
	p.maxBlockSize = p.blockSizeBuffer.GetMax()
	p.numSamples = p.blockSizeBuffer.Size()

	// Use defaults if not enough samples
	if p.numSamples < minSamplesForDynamic {
		p.maxPendingPerPeer = defaultMaxPendingRequestsPerPeer
		p.retryTimeout = time.Duration(defaultRetrySeconds * float64(time.Second))
	} else {
		// Use max block size for calculations (worst-case planning)
		p.maxPendingPerPeer = p.calculateMaxPending(p.maxBlockSize)
		p.retryTimeout = p.calculateRetryTimeout(p.maxBlockSize)
	}

	// Calculate requesters limit based on max block size (worst-case memory usage)
	p.peerBasedLimit = numPeers * p.maxPendingPerPeer
	p.memoryBasedLimit = p.maxRequesters
	if p.maxBlockSize > 0 {
		p.memoryBasedLimit = int(maxMemoryForRequesters / p.maxBlockSize)
	}
	p.requestersLimit = RequesterLimitHardCap
	p.maxPendingPerPeer = MaxPendingHardCap
}

// addBlock updates parameters after adding a new block
// This triggers recalculation of all dynamic parameters based on the max block size
func (p *BlockPoolParams) addBlock(blockSize int, numPeers int) {
	// Track block size
	p.blockSizeBuffer.Add(float64(blockSize))

	// Recalculate all parameters with new max
	p.recalculate(numPeers)
}

func (p *BlockPoolParams) calculateMaxPending(blockSize float64) int {
	// Clamp to min/max bounds
	if blockSize <= minBlockSizeBytes {
		return maxPendingForSmallBlocks
	}
	if blockSize >= maxBlockSizeBytes {
		return maxPendingForLargeBlocks
	}

	// Calculate normalized position [0, 1] in the range
	normalized := (blockSize - minBlockSizeBytes) / (maxBlockSizeBytes - minBlockSizeBytes)

	// Inverse linear: as block size increases, max pending decreases
	rawMaxPending := maxPendingForSmallBlocks - int(float64(maxPendingForSmallBlocks-maxPendingForLargeBlocks)*normalized)

	// Round to nearest minRequesterIncrease of 5 for ladder effect
	step := minRequesterIncrease
	maxPending := ((rawMaxPending + step/2) / step) * step

	// Ensure we don't go below minimum
	if maxPending < maxPendingForLargeBlocks {
		maxPending = maxPendingForLargeBlocks
	}

	return maxPending
}

// calculateRetryTimeout returns retry timeout based on block size
func (p *BlockPoolParams) calculateRetryTimeout(blockSize float64) time.Duration {
	// Clamp to min/max bounds
	if blockSize <= minBlockSizeBytes {
		return time.Duration(minRetrySeconds * float64(time.Second))
	}
	if blockSize >= maxBlockSizeBytes {
		return time.Duration(maxRetrySeconds * float64(time.Second))
	}

	// Calculate normalized position [0, 1] in the range
	normalized := (blockSize - minBlockSizeBytes) / (maxBlockSizeBytes - minBlockSizeBytes)

	// Calculate retry seconds
	retrySeconds := minRetrySeconds + (maxRetrySeconds-minRetrySeconds)*normalized

	return time.Duration(retrySeconds * float64(time.Second))
}
