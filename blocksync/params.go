package blocksync

import (
	"time"
)

var (
	RequesterLimitHardCap = 40
	MaxPendingHardCap     = 5
)

// interpolate performs an inverse-linear interpolation between two integer
// bounds based on a floating-point input within a numeric range.
//
// Arguments:
//
//	value:      The input value to interpolate over.
//	minValue:   The minimum value of the input range.
//	maxValue:   The maximum value of the input range.
//	maxOut:     The output when value <= minValue.
//	minOut:     The output when value >= maxValue.
//
// Behavior:
//   - Clamps the input into [minValue, maxValue].
//   - Maps the input inversely from maxOut → minOut.
//   - Ensures the output does not fall below minOut.
func interpolate(value, minValue, maxValue int, maxOut, minOut int) int {
	// Clamp to lower bound
	if value <= minValue {
		return maxOut
	}
	// Clamp to upper bound
	if value >= maxValue {
		return minOut
	}

	// Normalize into [0, 1]
	normalized := float64(value-minValue) / float64(maxValue-minValue)

	// Inverse linear interpolation from maxOut → minOut
	raw := maxOut - int(float64(maxOut-minOut)*normalized)

	// Floor to minimum
	if raw < minOut {
		raw = minOut
	}

	return raw
}

// BlockPoolParams holds dynamically calculated parameters for the block pool
type BlockPoolParams struct {
	reqLimit        int
	retryTimeout    time.Duration
	blockSizeBuffer *blockStats

	// Cached calculated values for logging
	blockSize int
}

// newBlockPoolParams creates a new BlockPoolParams with the given configuration
func newBlockPoolParams(blockSizeBuffer *blockStats, maxRequesters int) *BlockPoolParams {
	params := &BlockPoolParams{
		blockSizeBuffer: blockSizeBuffer,
	}
	params.recalculate()
	return params
}

// recalculate updates all parameters based on current conditions
// numPeers is passed in since it's external state from BlockPool
func (p *BlockPoolParams) recalculate() {
	p.blockSize = int(p.blockSizeBuffer.GetMax())
	if p.blockSizeBuffer.Size() == 0 {
		p.reqLimit = maxReqLimit / 2
		p.retryTimeout = time.Duration(maxRetrySeconds / 2 * float64(time.Second))
	} else {
		p.reqLimit = interpolate(p.blockSize, minBlockSizeBytes, maxBlockSizeBytes, maxReqLimit, minReqLimit)
		p.retryTimeout = time.Duration(
			interpolate(p.blockSize, minBlockSizeBytes, maxBlockSizeBytes, minRetrySeconds, maxRetrySeconds))
	}
}

// addBlock updates parameters after adding a new block
// This triggers recalculation of all dynamic parameters based on the max block size
func (p *BlockPoolParams) addBlock(blockSize int, numPeers int) {
	// Track block size
	p.blockSizeBuffer.Add(float64(blockSize))

	// Recalculate all parameters with new max
	p.recalculate()
}
