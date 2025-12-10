package propagation

import (
	"bytes"
	"errors"
	"time"

	"github.com/cometbft/cometbft/crypto/merkle"
	"github.com/cometbft/cometbft/libs/bits"
	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/libs/sync"
	"github.com/cometbft/cometbft/types"

	proptypes "github.com/cometbft/cometbft/consensus/propagation/types"
	"github.com/cometbft/cometbft/headersync"
)

var (
	ErrNoCapacity       = errors.New("no capacity for new block")
	ErrUnknownHeight    = errors.New("part for unknown height")
	ErrBlockNotActive   = errors.New("block not active for downloads")
	ErrDuplicateBlock   = errors.New("duplicate compact block")
	ErrBlockIDMismatch  = errors.New("compact block hash doesn't match verified header")
	ErrInvalidPartIndex = errors.New("part index out of range")
)

// PendingBlockState tracks the download state of a block.
type PendingBlockState int

const (
	// BlockStateHeaderOnly - we have a verified header but no compact block yet.
	BlockStateHeaderOnly PendingBlockState = iota
	// BlockStateActive - we have a compact block and are downloading parts.
	BlockStateActive
	// BlockStateComplete - all original parts received.
	BlockStateComplete
)

// PendingBlock represents a block being downloaded.
// Created when either:
// 1. A compact block arrives AND headersync has verified its header
// 2. A verified header arrives from headersync (without compact block yet)
type PendingBlock struct {
	Height int64
	Round  int32

	// BlockID from verified header (if available).
	BlockID types.BlockID

	// Block data - the combined original + parity part set.
	Parts *proptypes.CombinedPartSet

	// Compact block metadata (nil if header arrived first without compact block).
	CompactBlock *proptypes.CompactBlock

	// Whether header has been verified by headersync.
	HeaderVerified bool

	// Timing for metrics/debugging.
	CreatedAt time.Time

	// State tracks download progress.
	State PendingBlockState
}

// PendingBlocksConfig holds configuration for PendingBlocksManager.
type PendingBlocksConfig struct {
	MaxConcurrent int   // Maximum concurrent block downloads (e.g., 5).
	MemoryBudget  int64 // Memory budget in bytes (e.g., 100MB).
}

// DefaultPendingBlocksConfig returns sensible defaults.
func DefaultPendingBlocksConfig() PendingBlocksConfig {
	return PendingBlocksConfig{
		MaxConcurrent: 5,
		MemoryBudget:  100 * 1024 * 1024, // 100 MB
	}
}

// HeaderVerifier provides access to verified headers from headersync.
type HeaderVerifier interface {
	// GetVerifiedHeader returns a verified header if available.
	// Returns the header, blockID, and whether it was found.
	GetVerifiedHeader(height int64) (*types.Header, *types.BlockID, bool)

	// Subscribe returns a channel that receives newly verified headers.
	Subscribe() <-chan *headersync.VerifiedHeader
}

// PendingBlocksManager manages concurrent block downloads.
// It routes parts to the correct block by height and prioritizes older blocks.
type PendingBlocksManager struct {
	mtx    sync.RWMutex
	logger log.Logger

	// Pending blocks indexed by height.
	blocks map[int64]*PendingBlock

	// Heights sorted ascending (lowest = highest priority).
	heights []int64

	// Constraints.
	config        PendingBlocksConfig
	currentMemory int64

	// Verified headers from headersync.
	headerVerifier HeaderVerifier

	// Output channel for forwarding parts to consensus.
	partsChan chan<- types.PartInfo
}

// NewPendingBlocksManager creates a new manager for concurrent block downloads.
func NewPendingBlocksManager(
	logger log.Logger,
	partsChan chan<- types.PartInfo,
	headerVerifier HeaderVerifier,
	config PendingBlocksConfig,
) *PendingBlocksManager {
	if config.MaxConcurrent <= 0 {
		config.MaxConcurrent = 5
	}
	if config.MemoryBudget <= 0 {
		config.MemoryBudget = 100 * 1024 * 1024
	}

	return &PendingBlocksManager{
		logger:         logger,
		blocks:         make(map[int64]*PendingBlock),
		heights:        make([]int64, 0),
		config:         config,
		headerVerifier: headerVerifier,
		partsChan:      partsChan,
	}
}

// HandleCompactBlock processes an incoming compact block.
// Returns nil if the block was accepted (or is a duplicate), error otherwise.
func (m *PendingBlocksManager) HandleCompactBlock(cb *proptypes.CompactBlock) error {
	m.mtx.Lock()
	defer m.mtx.Unlock()

	height := cb.Proposal.Height
	round := cb.Proposal.Round

	// Already have this block?
	if pending, exists := m.blocks[height]; exists {
		if pending.CompactBlock != nil {
			return nil // Duplicate, silently accept.
		}
		// We had header-only, now we have compact block - activate it.
		return m.activateBlockLocked(pending, cb)
	}

	// Check if headersync has verified the header.
	header, blockID, verified := m.getVerifiedHeader(height)
	if !verified {
		// Header not verified yet - this is the "live" case at chain tip.
		// The propagation reactor handles verification via proposer signature.
		//
		// TODO: Future enhancement - queue unverified compact blocks briefly
		// in case the header arrives shortly after. For now, return nil to
		// indicate we're not handling this block.
		return nil
	}

	// Header is verified - validate that compact block matches.
	if !bytes.Equal(blockID.Hash, cb.Proposal.BlockID.Hash) {
		return ErrBlockIDMismatch
	}

	return m.addVerifiedBlockLocked(cb, header, blockID, height, round)
}

// activateBlockLocked transitions a header-only block to active when compact block arrives.
// Caller must hold m.mtx.
func (m *PendingBlocksManager) activateBlockLocked(pending *PendingBlock, cb *proptypes.CompactBlock) error {
	// Verify compact block matches our verified header.
	if pending.HeaderVerified && !bytes.Equal(pending.BlockID.Hash, cb.Proposal.BlockID.Hash) {
		return ErrBlockIDMismatch
	}

	pending.CompactBlock = cb
	pending.Round = cb.Proposal.Round
	pending.Parts = proptypes.NewCombinedSetFromCompactBlock(cb)
	pending.State = BlockStateActive

	m.currentMemory += m.estimateMemory(cb)
	return nil
}

// addVerifiedBlockLocked adds a new block that has a verified header.
// Caller must hold m.mtx.
func (m *PendingBlocksManager) addVerifiedBlockLocked(
	cb *proptypes.CompactBlock,
	blockID *types.BlockID,
	height int64,
	round int32,
) error {
	if !m.hasCapacityLocked(cb) {
		return ErrNoCapacity
	}

	pending := &PendingBlock{
		Height:         height,
		Round:          round,
		BlockID:        *blockID,
		CompactBlock:   cb,
		Parts:          proptypes.NewCombinedSetFromCompactBlock(cb),
		HeaderVerified: true,
		CreatedAt:      time.Now(),
		State:          BlockStateActive,
	}

	m.blocks[height] = pending
	m.insertHeightLocked(height)
	m.currentMemory += m.estimateMemory(cb)

	return nil
}

// HandlePart routes a received part to the appropriate pending block.
// Returns error if part cannot be handled (unknown height, invalid, etc).
func (m *PendingBlocksManager) HandlePart(
	height int64,
	round int32,
	part *proptypes.RecoveryPart,
	proof merkle.Proof,
) (added bool, err error) {
	m.mtx.Lock()
	defer m.mtx.Unlock()

	pending, exists := m.blocks[height]
	if !exists {
		return false, ErrUnknownHeight
	}

	if pending.State != BlockStateActive {
		return false, ErrBlockNotActive
	}

	if pending.Round != round {
		// Part is for a different round - ignore.
		return false, nil
	}

	// Add part to combined part set (handles original vs parity routing).
	added, err = pending.Parts.AddPart(part, proof)
	if err != nil {
		return false, err
	}

	if !added {
		return false, nil // Duplicate part.
	}

	// Forward original parts to consensus.
	if part.Index < pending.Parts.Original().Total() {
		m.forwardPartToConsensus(part, proof, height, round)
	}

	// Check if we have all original parts.
	if pending.Parts.Original().IsComplete() {
		pending.State = BlockStateComplete
	}

	return true, nil
}

// forwardPartToConsensus sends a part to the consensus reactor.
func (m *PendingBlocksManager) forwardPartToConsensus(
	part *proptypes.RecoveryPart,
	proof merkle.Proof,
	height int64,
	round int32,
) {
	select {
	case m.partsChan <- types.PartInfo{
		Part: &types.Part{
			Index: part.Index,
			Bytes: part.Data,
			Proof: proof,
		},
		Height: height,
		Round:  round,
	}:
	default:
		m.logger.Debug("parts channel full, dropping part",
			"height", height, "round", round, "index", part.Index)
	}
}

// OnHeaderVerified handles notification that a header has been verified by headersync.
// This may activate a pending header-only block or create a new one.
func (m *PendingBlocksManager) OnHeaderVerified(vh *headersync.VerifiedHeader) {
	m.mtx.Lock()
	defer m.mtx.Unlock()

	height := vh.Header.Height

	// Already tracking this height?
	if pending, exists := m.blocks[height]; exists {
		// Update verification status if we had an unverified compact block.
		if !pending.HeaderVerified {
			if pending.CompactBlock != nil && !bytes.Equal(vh.BlockID.Hash, pending.CompactBlock.Proposal.BlockID.Hash) {
				// Compact block we had was wrong - evict it.
				m.logger.Error("compact block hash mismatch with verified header, evicting",
					"height", height,
					"compact_hash", pending.CompactBlock.Proposal.BlockID.Hash,
					"verified_hash", vh.BlockID.Hash)
				m.evictBlockLocked(height)
				return
			}
			pending.BlockID = vh.BlockID
			pending.HeaderVerified = true
		}
		return
	}

	// No compact block yet - create header-only entry if we have capacity.
	// This allows us to be ready when the compact block arrives.
	if !m.hasCapacityLocked(nil) {
		return
	}

	pending := &PendingBlock{
		Height:         height,
		BlockID:        vh.BlockID,
		HeaderVerified: true,
		CreatedAt:      time.Now(),
		State:          BlockStateHeaderOnly,
	}

	m.blocks[height] = pending
	m.insertHeightLocked(height)
}

// GetMissingParts returns parts that still need to be requested for active blocks.
// Parts are returned grouped by height, in priority order (lowest height first).
func (m *PendingBlocksManager) GetMissingParts() []MissingPartsInfo {
	m.mtx.RLock()
	defer m.mtx.RUnlock()

	result := make([]MissingPartsInfo, 0, len(m.heights))

	for _, height := range m.heights {
		pending := m.blocks[height]
		if pending.State != BlockStateActive {
			continue
		}

		missing := pending.Parts.MissingOriginal()
		indices := missing.GetTrueIndices()
		if len(indices) == 0 {
			continue
		}

		result = append(result, MissingPartsInfo{
			Height:         height,
			Round:          pending.Round,
			MissingIndices: indices,
			Priority:       m.getPriorityWeight(height),
		})
	}

	return result
}

// MissingPartsInfo describes parts needed for a block.
type MissingPartsInfo struct {
	Height         int64
	Round          int32
	MissingIndices []int
	Priority       float64 // Higher = more important (oldest blocks get 1.0).
}

// GetBlock returns the pending block at the given height, if it exists.
func (m *PendingBlocksManager) GetBlock(height int64) (*PendingBlock, bool) {
	m.mtx.RLock()
	defer m.mtx.RUnlock()

	pending, exists := m.blocks[height]
	return pending, exists
}

// DeleteHeight removes a block from tracking (e.g., after commit).
func (m *PendingBlocksManager) DeleteHeight(height int64) {
	m.mtx.Lock()
	defer m.mtx.Unlock()

	m.evictBlockLocked(height)
}

// Prune removes all blocks below the given height.
func (m *PendingBlocksManager) Prune(minHeight int64) {
	m.mtx.Lock()
	defer m.mtx.Unlock()

	for height := range m.blocks {
		if height < minHeight {
			m.evictBlockLocked(height)
		}
	}
}

// Heights returns the currently tracked heights in priority order.
func (m *PendingBlocksManager) Heights() []int64 {
	m.mtx.RLock()
	defer m.mtx.RUnlock()

	result := make([]int64, len(m.heights))
	copy(result, m.heights)
	return result
}

// Len returns the number of blocks being tracked.
func (m *PendingBlocksManager) Len() int {
	m.mtx.RLock()
	defer m.mtx.RUnlock()
	return len(m.blocks)
}

// getVerifiedHeader checks if headersync has verified a header.
func (m *PendingBlocksManager) getVerifiedHeader(height int64) (*types.Header, *types.BlockID, bool) {
	if m.headerVerifier == nil {
		return nil, nil, false
	}
	return m.headerVerifier.GetVerifiedHeader(height)
}

// hasCapacityLocked checks if we can add another block.
// Caller must hold m.mtx.
func (m *PendingBlocksManager) hasCapacityLocked(cb *proptypes.CompactBlock) bool {
	if len(m.blocks) >= m.config.MaxConcurrent {
		return false
	}

	if cb != nil {
		estimated := m.estimateMemory(cb)
		if m.currentMemory+estimated > m.config.MemoryBudget {
			return false
		}
	}

	return true
}

// estimateMemory estimates memory usage for a compact block.
func (m *PendingBlocksManager) estimateMemory(cb *proptypes.CompactBlock) int64 {
	if cb == nil {
		return 0
	}
	// Estimate: total parts * part size.
	partSize := int64(types.BlockPartSizeBytes)
	totalParts := int64(cb.Proposal.BlockID.PartSetHeader.Total * 2) // Original + parity.
	return totalParts * partSize
}

// insertHeightLocked inserts height in sorted order.
// Caller must hold m.mtx.
func (m *PendingBlocksManager) insertHeightLocked(height int64) {
	// Binary search for insertion point.
	i := 0
	for i < len(m.heights) && m.heights[i] < height {
		i++
	}

	// Insert at position i.
	m.heights = append(m.heights, 0)
	copy(m.heights[i+1:], m.heights[i:])
	m.heights[i] = height
}

// evictBlockLocked removes a block and reclaims its memory.
// Caller must hold m.mtx.
func (m *PendingBlocksManager) evictBlockLocked(height int64) {
	pending, exists := m.blocks[height]
	if !exists {
		return
	}

	// Reclaim memory.
	if pending.CompactBlock != nil {
		m.currentMemory -= m.estimateMemory(pending.CompactBlock)
	}

	delete(m.blocks, height)

	// Remove from sorted heights.
	for i, h := range m.heights {
		if h == height {
			m.heights = append(m.heights[:i], m.heights[i+1:]...)
			break
		}
	}
}

// getPriorityWeight returns priority weight for a height.
// Lowest height gets weight 1.0, each subsequent halves.
func (m *PendingBlocksManager) getPriorityWeight(height int64) float64 {
	if len(m.heights) == 0 {
		return 1.0
	}

	idx := 0
	for i, h := range m.heights {
		if h == height {
			idx = i
			break
		}
	}

	// Exponential decay: priority 0 = 1.0, priority 1 = 0.5, priority 2 = 0.25.
	return 1.0 / float64(int(1)<<idx)
}

// --- Request tracking helper (used by reactor for scheduling) ---

// RequestTracker tracks outstanding part requests for a single block.
type RequestTracker struct {
	mtx       sync.Mutex
	total     int
	requested *bits.BitArray // Parts we've requested.
	received  *bits.BitArray // Parts we've received.
	reqCounts []int          // Number of times each part requested.
	reqLimit  int            // Max requests per part.
}

// NewRequestTracker creates a tracker for a block with the given part count.
func NewRequestTracker(total int, reqLimit int) *RequestTracker {
	return &RequestTracker{
		total:     total,
		requested: bits.NewBitArray(total),
		received:  bits.NewBitArray(total),
		reqCounts: make([]int, total),
		reqLimit:  reqLimit,
	}
}

// MarkRequested marks a part as requested.
func (rt *RequestTracker) MarkRequested(index uint32) {
	rt.mtx.Lock()
	defer rt.mtx.Unlock()

	if int(index) >= rt.total {
		return
	}
	rt.requested.SetIndex(int(index), true)
	rt.reqCounts[index]++
}

// MarkReceived marks a part as received.
func (rt *RequestTracker) MarkReceived(index uint32) {
	rt.mtx.Lock()
	defer rt.mtx.Unlock()

	if int(index) >= rt.total {
		return
	}
	rt.received.SetIndex(int(index), true)
}

// GetUnrequestedMissing returns indices of parts that are missing and haven't been requested.
func (rt *RequestTracker) GetUnrequestedMissing() []int {
	rt.mtx.Lock()
	defer rt.mtx.Unlock()

	result := make([]int, 0)
	for i := 0; i < rt.total; i++ {
		if !rt.received.GetIndex(i) && !rt.requested.GetIndex(i) {
			result = append(result, i)
		}
	}
	return result
}

// GetRetryableMissing returns indices of parts that can be re-requested (under limit).
func (rt *RequestTracker) GetRetryableMissing() []int {
	rt.mtx.Lock()
	defer rt.mtx.Unlock()

	result := make([]int, 0)
	for i := 0; i < rt.total; i++ {
		if !rt.received.GetIndex(i) && rt.reqCounts[i] < rt.reqLimit {
			result = append(result, i)
		}
	}
	return result
}

// IsComplete returns true if all parts have been received.
func (rt *RequestTracker) IsComplete() bool {
	rt.mtx.Lock()
	defer rt.mtx.Unlock()
	return rt.received.IsFull()
}
