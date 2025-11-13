package blocksync

import (
	"time"
)

// poolConfig holds configuration for block pool parameter calculations
type poolConfig struct {
	// Border values for dynamic retry timer calculation
	minBlockSizeBytes float64
	maxBlockSizeBytes float64
	minRetrySeconds   float64
	maxRetrySeconds   float64

	// Border values for dynamic maxPendingRequestsPerPeer
	maxPendingForSmallBlocks int
	maxPendingForLargeBlocks int

	// Minimum samples before using dynamic values
	minSamplesForDynamic int

	// Default values when not enough samples
	defaultMaxPendingPerPeer int
	defaultRetrySeconds      float64

	// Maximum memory to use for pending block requests
	maxMemoryForRequesters float64

	// ladder step to which we round the value of requesters, e.g. 31 will be rounded to 30
	// if step is 5
	step int
}

// BlockPoolParams holds dynamically calculated parameters for the block pool
type BlockPoolParams struct {
	config            poolConfig
	maxPendingPerPeer int
	retryTimeout      time.Duration
	requestersLimit   int
	blockSizeBuffer   *RotatingBuffer
	maxRequesters     int

	// Cached calculated values for logging
	avgBlockSize     float64 // kept for logging purposes
	maxBlockSize     float64 // used for calculations
	peerBasedLimit   int
	memoryBasedLimit int
	numSamples       int
}

// NewBlockPoolParams creates a new BlockPoolParams with the given configuration
func NewBlockPoolParams(config poolConfig, blockSizeBuffer *RotatingBuffer, maxRequesters int) *BlockPoolParams {
	params := &BlockPoolParams{
		config:          config,
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
	if p.numSamples < p.config.minSamplesForDynamic {
		p.maxPendingPerPeer = p.config.defaultMaxPendingPerPeer
		p.retryTimeout = time.Duration(p.config.defaultRetrySeconds * float64(time.Second))
	} else {
		// Use max block size for calculations (worst-case planning)
		p.maxPendingPerPeer = p.calculateMaxPendingLadder(p.maxBlockSize)
		p.retryTimeout = p.calculateRetryTimeout(p.maxBlockSize)
	}

	// Calculate requesters limit based on max block size (worst-case memory usage)
	p.peerBasedLimit = numPeers * p.maxPendingPerPeer
	p.memoryBasedLimit = p.maxRequesters
	if p.maxBlockSize > 0 {
		p.memoryBasedLimit = int(p.config.maxMemoryForRequesters / p.maxBlockSize)
	}
	p.requestersLimit = min(p.peerBasedLimit, p.memoryBasedLimit, p.maxRequesters)
}

// addBlock updates parameters after adding a new block
// This triggers recalculation of all dynamic parameters based on the max block size
func (p *BlockPoolParams) addBlock(blockSize int, numPeers int) {
	// Track block size
	p.blockSizeBuffer.Add(float64(blockSize))

	// Recalculate all parameters with new max
	p.recalculate(numPeers)
}

// calculateMaxPendingLadder returns maxPending in discrete steps (ladder effect)
// Returns values in steps of 5: 40, 35, 30, 25, 20, 15, 10, 5, 2
func (p *BlockPoolParams) calculateMaxPendingLadder(blockSize float64) int {
	// Clamp to min/max bounds
	if blockSize <= p.config.minBlockSizeBytes {
		return p.config.maxPendingForSmallBlocks
	}
	if blockSize >= p.config.maxBlockSizeBytes {
		return p.config.maxPendingForLargeBlocks
	}

	// Calculate normalized position [0, 1] in the range
	normalized := (blockSize - p.config.minBlockSizeBytes) / (p.config.maxBlockSizeBytes - p.config.minBlockSizeBytes)

	// Inverse linear: as block size increases, max pending decreases
	rawMaxPending := p.config.maxPendingForSmallBlocks - int(float64(p.config.maxPendingForSmallBlocks-p.config.maxPendingForLargeBlocks)*normalized)

	// Round to nearest step of 5 for ladder effect
	step := p.config.step
	maxPending := ((rawMaxPending + step/2) / step) * step

	// Ensure we don't go below minimum
	if maxPending < p.config.maxPendingForLargeBlocks {
		maxPending = p.config.maxPendingForLargeBlocks
	}

	return maxPending
}

// calculateRetryTimeout returns retry timeout based on block size
func (p *BlockPoolParams) calculateRetryTimeout(blockSize float64) time.Duration {
	// Clamp to min/max bounds
	if blockSize <= p.config.minBlockSizeBytes {
		return time.Duration(p.config.minRetrySeconds * float64(time.Second))
	}
	if blockSize >= p.config.maxBlockSizeBytes {
		return time.Duration(p.config.maxRetrySeconds * float64(time.Second))
	}

	// Calculate normalized position [0, 1] in the range
	normalized := (blockSize - p.config.minBlockSizeBytes) / (p.config.maxBlockSizeBytes - p.config.minBlockSizeBytes)

	// Calculate retry seconds
	retrySeconds := p.config.minRetrySeconds + (p.config.maxRetrySeconds-p.config.minRetrySeconds)*normalized

	return time.Duration(retrySeconds * float64(time.Second))
}
