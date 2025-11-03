package blocksync

import (
	"fmt"
	"sync"
	"time"

	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/p2p"
	"github.com/cometbft/cometbft/types"
)

// BlockBuffer implements a sliding window buffer for blocks during sync.
// It ensures that only blocks within [baseHeight, baseHeight+windowSize) can be added.
// This prevents unbounded memory growth during block sync.
type BlockBuffer struct {
	logger log.Logger
	mtx    sync.RWMutex

	// Window parameters
	baseHeight int64 // Minimum height in the window (X)
	windowSize int64 // Maximum window size (N)

	// Block storage - only contains blocks in [baseHeight, baseHeight+windowSize)
	blocks map[int64]*BufferedBlock

	// Metrics
	highestHeight int64 // Highest block height currently in buffer
}

// BufferedBlock wraps a block with metadata about when and from whom it was received
type BufferedBlock struct {
	Block      *types.Block
	ExtCommit  *types.ExtendedCommit
	PeerID     p2p.ID
	ReceivedAt time.Time
	Size       int // Block size in bytes
}

// NewBlockBuffer creates a new sliding window buffer starting at the given height
func NewBlockBuffer(startHeight int64, windowSize int64, logger log.Logger) *BlockBuffer {
	return &BlockBuffer{
		logger:        logger,
		baseHeight:    startHeight,
		windowSize:    windowSize,
		blocks:        make(map[int64]*BufferedBlock),
		highestHeight: startHeight - 1,
	}
}

// AddBlock attempts to add a block to the buffer.
// Returns error if block is outside the sliding window.
func (bb *BlockBuffer) AddBlock(height int64, block *types.Block, extCommit *types.ExtendedCommit, peerID p2p.ID, blockSize int) error {
	bb.mtx.Lock()
	defer bb.mtx.Unlock()

	// Check if block is within sliding window
	if height < bb.baseHeight {
		return fmt.Errorf("block height %d is below window base %d (already processed)", height, bb.baseHeight)
	}

	if height >= bb.baseHeight+bb.windowSize {
		return fmt.Errorf("block height %d is outside window [%d, %d) - sliding window full",
			height, bb.baseHeight, bb.baseHeight+bb.windowSize)
	}

	// Check if already have this block
	if _, exists := bb.blocks[height]; exists {
		bb.logger.Debug("Block already in buffer", "height", height)
		return nil // Not an error - duplicate from second peer
	}

	// Add to buffer
	bb.blocks[height] = &BufferedBlock{
		Block:      block,
		ExtCommit:  extCommit,
		PeerID:     peerID,
		ReceivedAt: time.Now(),
		Size:       blockSize,
	}

	if height > bb.highestHeight {
		bb.highestHeight = height
	}

	bb.logger.Debug("Block added to buffer",
		"height", height,
		"peer", peerID,
		"buffer_size", len(bb.blocks),
		"window", fmt.Sprintf("[%d, %d)", bb.baseHeight, bb.baseHeight+bb.windowSize))

	return nil
}

// PeekNext returns the next sequential block (at baseHeight) if available.
// Returns nil if block at baseHeight is not yet in buffer.
// Does NOT remove the block from buffer.
func (bb *BlockBuffer) PeekNext() *BufferedBlock {
	bb.mtx.RLock()
	defer bb.mtx.RUnlock()

	return bb.blocks[bb.baseHeight]
}

// PeekTwoNext returns the next two sequential blocks if both are available.
// This is needed for validation (need block N+1's commit to verify block N).
// Returns nil if either block is not available.
func (bb *BlockBuffer) PeekTwoNext() (first, second *BufferedBlock) {
	bb.mtx.RLock()
	defer bb.mtx.RUnlock()

	first = bb.blocks[bb.baseHeight]
	second = bb.blocks[bb.baseHeight+1]

	if first == nil || second == nil {
		return nil, nil
	}

	return first, second
}

// PopNext removes and returns the block at baseHeight, advancing the window by 1.
// Returns nil if no block at baseHeight.
func (bb *BlockBuffer) PopNext() *BufferedBlock {
	bb.mtx.Lock()
	defer bb.mtx.Unlock()

	block := bb.blocks[bb.baseHeight]
	if block == nil {
		return nil
	}

	// Remove from buffer
	delete(bb.blocks, bb.baseHeight)

	// Advance window
	bb.baseHeight++

	bb.logger.Debug("Block popped from buffer",
		"height", bb.baseHeight-1,
		"new_base", bb.baseHeight,
		"buffer_size", len(bb.blocks))

	return block
}

// HasBlock returns true if the buffer contains a block at the given height
func (bb *BlockBuffer) HasBlock(height int64) bool {
	bb.mtx.RLock()
	defer bb.mtx.RUnlock()

	_, exists := bb.blocks[height]
	return exists
}

// IsInWindow returns true if the given height is within the current sliding window
func (bb *BlockBuffer) IsInWindow(height int64) bool {
	bb.mtx.RLock()
	defer bb.mtx.RUnlock()

	return height >= bb.baseHeight && height < bb.baseHeight+bb.windowSize
}

// BaseHeight returns the current base height of the sliding window
func (bb *BlockBuffer) BaseHeight() int64 {
	bb.mtx.RLock()
	defer bb.mtx.RUnlock()

	return bb.baseHeight
}

// WindowSize returns the configured window size
func (bb *BlockBuffer) WindowSize() int64 {
	bb.mtx.RLock()
	defer bb.mtx.RUnlock()

	return bb.windowSize
}

// MaxHeight returns the maximum height that can be added to the buffer
func (bb *BlockBuffer) MaxHeight() int64 {
	bb.mtx.RLock()
	defer bb.mtx.RUnlock()

	return bb.baseHeight + bb.windowSize - 1
}

// Size returns the current number of blocks in the buffer
func (bb *BlockBuffer) Size() int {
	bb.mtx.RLock()
	defer bb.mtx.RUnlock()

	return len(bb.blocks)
}

// Stats returns statistics about the buffer state
func (bb *BlockBuffer) Stats() BufferStats {
	bb.mtx.RLock()
	defer bb.mtx.RUnlock()

	stats := BufferStats{
		BaseHeight:    bb.baseHeight,
		WindowSize:    bb.windowSize,
		BufferSize:    len(bb.blocks),
		HighestHeight: bb.highestHeight,
	}

	// Calculate total memory usage
	for _, block := range bb.blocks {
		stats.TotalBytes += int64(block.Size)
	}

	// Find gaps
	for h := bb.baseHeight; h < bb.baseHeight+bb.windowSize; h++ {
		if _, exists := bb.blocks[h]; !exists {
			stats.MissingHeights = append(stats.MissingHeights, h)
		}
	}

	return stats
}

// BufferStats contains statistics about the buffer state
type BufferStats struct {
	BaseHeight     int64
	WindowSize     int64
	BufferSize     int
	HighestHeight  int64
	TotalBytes     int64
	MissingHeights []int64
}

// Clear removes all blocks from the buffer and resets to the given base height
func (bb *BlockBuffer) Clear(newBaseHeight int64) {
	bb.mtx.Lock()
	defer bb.mtx.Unlock()

	bb.blocks = make(map[int64]*BufferedBlock)
	bb.baseHeight = newBaseHeight
	bb.highestHeight = newBaseHeight - 1

	bb.logger.Info("Buffer cleared", "new_base_height", newBaseHeight)
}

// RemoveBlock removes a specific block from the buffer without advancing the window.
// Used when a block fails validation and needs to be re-requested.
func (bb *BlockBuffer) RemoveBlock(height int64) {
	bb.mtx.Lock()
	defer bb.mtx.Unlock()

	if _, exists := bb.blocks[height]; exists {
		delete(bb.blocks, height)
		bb.logger.Debug("Block removed from buffer", "height", height)
	}
}

// RemoveBlocksFromPeer removes all blocks that were received from a specific peer.
// Returns the number of blocks removed.
func (bb *BlockBuffer) RemoveBlocksFromPeer(peerID p2p.ID) int {
	bb.mtx.Lock()
	defer bb.mtx.Unlock()

	removed := 0
	for height, block := range bb.blocks {
		if block.PeerID == peerID {
			delete(bb.blocks, height)
			removed++
			bb.logger.Debug("Block from peer removed from buffer", "height", height, "peer", peerID)
		}
	}

	if removed > 0 {
		bb.logger.Info("Removed blocks from peer", "peer", peerID, "count", removed)
	}

	return removed
}
