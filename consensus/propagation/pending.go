package propagation

import (
	"bytes"
	"errors"
	"sync/atomic"
	"time"

	"github.com/cometbft/cometbft/crypto/merkle"
	"github.com/cometbft/cometbft/libs/bits"
	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/libs/sync"
	"github.com/cometbft/cometbft/store"
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

// BlockSource identifies how a block was added to the manager.
type BlockSource int

const (
	// SourceCompactBlock - block added via compact block from proposal gossip.
	SourceCompactBlock BlockSource = iota
	// SourceHeaderSync - block added via verified header from headersync.
	SourceHeaderSync
	// SourceCommitment - block added via PartSetHeader from consensus commit.
	SourceCommitment
)

func (s BlockSource) String() string {
	switch s {
	case SourceCompactBlock:
		return "compact_block"
	case SourceHeaderSync:
		return "header_sync"
	case SourceCommitment:
		return "commitment"
	default:
		return "unknown"
	}
}

// PendingBlockState tracks the download state of a block.
type PendingBlockState int

const (
	// BlockStateActive - we have block metadata and are downloading parts.
	BlockStateActive PendingBlockState = iota
	// BlockStateComplete - all original parts received.
	BlockStateComplete
)

// PendingBlock represents a block being downloaded or served.
// This unified structure handles both "live" blocks (from compact blocks)
// and "catchup" blocks (from verified headers or commitments).
type PendingBlock struct {
	Height int64
	Round  int32

	// Source indicates how this block was added.
	Source BlockSource

	// BlockID from verified header (if available).
	BlockID types.BlockID

	// Block data - the combined original + parity part set.
	Parts *proptypes.CombinedPartSet

	// Compact block metadata (nil for catchup-only blocks).
	CompactBlock *proptypes.CompactBlock

	// Request tracking for this block.
	MaxRequests *bits.BitArray

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
}

// BlockAddedCallback is called when a new block is added to the manager.
// This allows the reactor to trigger catchup immediately when needed.
type BlockAddedCallback func(height int64, source BlockSource)

// PendingBlocksManager manages all block downloads - both live and catchup.
// This is the unified manager for block propagation state.
//
// Data Entry Points:
//   - AddProposal: Live compact block from gossip (has parity data)
//   - AddFromHeader: Verified header from headersync (catchup, no parity)
//   - AddFromCommitment: PartSetHeader from consensus commit (catchup, no parity)
//
// All entry points converge to the same internal state machine.
type PendingBlocksManager struct {
	mtx    sync.RWMutex
	logger log.Logger

	// Block store for loading committed blocks.
	store *store.BlockStore

	// Pending blocks indexed by height.
	blocks map[int64]*PendingBlock

	// Heights sorted ascending (lowest = highest priority).
	heights []int64

	// Current consensus height and round.
	height int64
	round  int32

	// Constraints.
	config        PendingBlocksConfig
	currentMemory int64

	// Verified headers from headersync.
	headerVerifier HeaderVerifier

	// Output channel for forwarding parts to consensus.
	partsChan chan<- types.PartInfo

	// Callback when a block is added (for triggering catchup).
	onBlockAdded BlockAddedCallback

	// Current proposal parts count for request limiting.
	currentProposalPartsCount atomic.Int64
}

// NewPendingBlocksManager creates a new unified manager for block downloads.
func NewPendingBlocksManager(
	logger log.Logger,
	store *store.BlockStore,
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

	m := &PendingBlocksManager{
		logger:         logger,
		store:          store,
		blocks:         make(map[int64]*PendingBlock),
		heights:        make([]int64, 0),
		config:         config,
		headerVerifier: headerVerifier,
		partsChan:      partsChan,
	}

	// Initialize height from store.
	if store != nil && store.Height() != 0 {
		m.height = store.Height()
	}

	return m
}

// SetOnBlockAdded sets the callback for when a block is added.
func (m *PendingBlocksManager) SetOnBlockAdded(cb BlockAddedCallback) {
	m.mtx.Lock()
	defer m.mtx.Unlock()
	m.onBlockAdded = cb
}

// =============================================================================
// Block Entry Points - Three ways to add a block
// =============================================================================

// AddProposal adds a new compact block from live proposal gossip.
// This is the "hot path" for live consensus - blocks have parity data.
// Returns true if the block was added.
func (m *PendingBlocksManager) AddProposal(cb *proptypes.CompactBlock) bool {
	m.mtx.Lock()
	defer m.mtx.Unlock()

	height := cb.Proposal.Height
	round := cb.Proposal.Round

	if !m.relevantLocked(height, round) {
		return false
	}

	// Check if we already have this height.
	if pending, exists := m.blocks[height]; exists {
		// If we already have a compact block, skip.
		if pending.CompactBlock != nil {
			return false
		}
		// We had a header-only entry, attach the compact block.
		return m.attachCompactBlockLocked(pending, cb)
	}

	// Check capacity before adding new block.
	if !m.hasCapacityLocked(cb) {
		return false
	}

	// Create new pending block.
	m.height = height
	m.round = round

	parts := proptypes.NewCombinedSetFromCompactBlock(cb)
	pending := &PendingBlock{
		Height:       height,
		Round:        round,
		Source:       SourceCompactBlock,
		CompactBlock: cb,
		Parts:        parts,
		MaxRequests:  bits.NewBitArray(int(parts.Total())),
		CreatedAt:    time.Now(),
		State:        BlockStateActive,
	}

	// Check if header is verified.
	if m.headerVerifier != nil {
		_, blockID, verified := m.headerVerifier.GetVerifiedHeader(height)
		if verified {
			pending.BlockID = *blockID
			pending.HeaderVerified = true
		}
	}

	m.blocks[height] = pending
	m.insertHeightLocked(height)
	m.currentMemory += m.estimateMemory(cb)
	m.currentProposalPartsCount.Store(int64(parts.Total()))

	return true
}

// AddFromHeader adds a block for download from a verified header.
// This is the headersync catchup path - blocks need all original parts.
func (m *PendingBlocksManager) AddFromHeader(vh *headersync.VerifiedHeader) bool {
	m.mtx.Lock()

	height := vh.Header.Height

	// Already tracking this height?
	if pending, exists := m.blocks[height]; exists {
		if !pending.HeaderVerified {
			// Verify any existing compact block matches.
			if pending.CompactBlock != nil && !bytes.Equal(vh.BlockID.Hash, pending.CompactBlock.Proposal.BlockID.Hash) {
				m.logger.Error("compact block hash mismatch with verified header, evicting",
					"height", height,
					"compact_hash", pending.CompactBlock.Proposal.BlockID.Hash,
					"verified_hash", vh.BlockID.Hash)
				m.evictBlockLocked(height)
				m.mtx.Unlock()
				return false
			}
			pending.BlockID = vh.BlockID
			pending.HeaderVerified = true
		}
		m.mtx.Unlock()
		return false // Already tracking, not a new addition
	}

	// Check capacity before adding.
	if !m.hasCapacityLocked(nil) {
		m.mtx.Unlock()
		return false
	}

	// Create an active block from the verified header.
	m.addFromBlockIDLocked(height, vh.BlockID, SourceHeaderSync)
	callback := m.onBlockAdded
	m.mtx.Unlock()

	// Notify callback outside lock.
	if callback != nil {
		callback(height, SourceHeaderSync)
	}
	return true
}

// AddFromCommitment adds a block for download from a consensus commit.
// This handles edge cases where consensus learns about a committed block
// before headersync verifies the header.
func (m *PendingBlocksManager) AddFromCommitment(height int64, round int32, psh *types.PartSetHeader) bool {
	m.mtx.Lock()

	// Already tracking this height?
	if pending, exists := m.blocks[height]; exists {
		// Update round if needed.
		if pending.Round == 0 && round != 0 {
			pending.Round = round
		}
		m.mtx.Unlock()
		return false // Already tracking, not a new addition
	}

	// Check capacity.
	if !m.hasCapacityLocked(nil) {
		m.logger.Debug("no capacity for commitment", "height", height)
		m.mtx.Unlock()
		return false
	}

	blockID := types.BlockID{PartSetHeader: *psh}
	m.logger.Info("adding commitment for download", "height", height, "round", round, "parts", psh.Total)
	m.addFromBlockIDLocked(height, blockID, SourceCommitment)

	// Update round.
	if pending, exists := m.blocks[height]; exists {
		pending.Round = round
	}

	callback := m.onBlockAdded
	m.mtx.Unlock()

	// Notify callback outside lock.
	if callback != nil {
		callback(height, SourceCommitment)
	}
	return true
}

// Deprecated: Use AddFromHeader instead. Kept for backward compatibility.
func (m *PendingBlocksManager) OnHeaderVerified(vh *headersync.VerifiedHeader) {
	m.AddFromHeader(vh)
}

// Deprecated: Use AddFromCommitment instead. Kept for backward compatibility.
func (m *PendingBlocksManager) AddCommitment(height int64, round int32, psh *types.PartSetHeader) {
	m.AddFromCommitment(height, round, psh)
}

// =============================================================================
// Internal Block Creation
// =============================================================================

// attachCompactBlockLocked attaches a compact block to a header-only entry.
func (m *PendingBlocksManager) attachCompactBlockLocked(pending *PendingBlock, cb *proptypes.CompactBlock) bool {
	// Verify hash matches if we have a verified header.
	if pending.HeaderVerified && !bytes.Equal(pending.BlockID.Hash, cb.Proposal.BlockID.Hash) {
		m.logger.Error("compact block hash mismatch with verified header",
			"height", pending.Height,
			"verified_hash", pending.BlockID.Hash,
			"compact_hash", cb.Proposal.BlockID.Hash)
		return false
	}

	pending.CompactBlock = cb
	pending.Round = cb.Proposal.Round
	pending.Source = SourceCompactBlock // Upgraded from catchup to live

	// Keep existing Parts if already allocated, otherwise create new.
	if pending.Parts == nil {
		pending.Parts = proptypes.NewCombinedSetFromCompactBlock(cb)
	}
	pending.MaxRequests = bits.NewBitArray(int(pending.Parts.Total()))
	pending.State = BlockStateActive

	m.height = pending.Height
	m.round = pending.Round
	m.currentMemory += m.estimateMemory(cb)
	m.currentProposalPartsCount.Store(int64(pending.Parts.Total()))

	return true
}

// addFromBlockIDLocked creates an active pending block from a BlockID.
func (m *PendingBlocksManager) addFromBlockIDLocked(height int64, blockID types.BlockID, source BlockSource) {
	original := types.NewPartSetFromHeader(blockID.PartSetHeader, types.BlockPartSizeBytes)
	parts := proptypes.NewCombinedPartSetFromOriginal(original, true)

	pending := &PendingBlock{
		Height:         height,
		Round:          0, // Unknown for catchup blocks.
		Source:         source,
		BlockID:        blockID,
		Parts:          parts,
		MaxRequests:    bits.NewBitArray(int(parts.Total())),
		HeaderVerified: source == SourceHeaderSync, // Only header sync provides verified headers.
		CreatedAt:      time.Now(),
		State:          BlockStateActive,
	}

	m.blocks[height] = pending
	m.insertHeightLocked(height)

	// Estimate memory.
	partSize := int64(types.BlockPartSizeBytes)
	totalParts := int64(blockID.PartSetHeader.Total * 2)
	m.currentMemory += totalParts * partSize
}

// =============================================================================
// Part Handling
// =============================================================================

// HandlePart routes a received part to the appropriate pending block.
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

	if pending.Round != round && pending.Round != 0 {
		return false, nil
	}

	// Add part to combined part set.
	added, err = pending.Parts.AddPart(part, proof)
	if err != nil {
		return false, err
	}

	if !added {
		return false, nil
	}

	// Forward original parts to consensus.
	if part.Index < pending.Parts.Original().Total() {
		m.forwardPartToConsensus(part, proof, height, round)
	}

	// Check completion.
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
	if m.partsChan == nil {
		return
	}
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

// =============================================================================
// Missing Parts / Request Scheduling
// =============================================================================

// GetMissingParts returns parts that still need to be requested for active blocks.
// Parts are returned grouped by height, in priority order (lowest height first).
func (m *PendingBlocksManager) GetMissingParts() []MissingPartsInfo {
	m.mtx.RLock()
	defer m.mtx.RUnlock()

	result := make([]MissingPartsInfo, 0, len(m.heights))

	for _, height := range m.heights {
		pending := m.blocks[height]
		if pending.State != BlockStateActive || pending.Parts == nil {
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
			TotalParts:     pending.Parts.Original().Total(),
			MissingIndices: indices,
			Priority:       m.getPriorityWeight(height),
			Catchup:        pending.Parts.IsCatchup(),
		})
	}

	return result
}

// UnfinishedHeights returns all blocks that are not complete (for catchup).
func (m *PendingBlocksManager) UnfinishedHeights() []*PendingBlock {
	m.mtx.RLock()
	defer m.mtx.RUnlock()

	result := make([]*PendingBlock, 0)
	for _, height := range m.heights {
		pending := m.blocks[height]
		if pending.State != BlockStateComplete && pending.Parts != nil && !pending.Parts.IsComplete() {
			result = append(result, pending)
		}
	}
	return result
}

// MissingPartsInfo describes parts needed for a block.
type MissingPartsInfo struct {
	Height         int64
	Round          int32
	TotalParts     uint32
	MissingIndices []int
	Priority       float64
	Catchup        bool // True if this is a catchup-only block (no parity data).
}

// =============================================================================
// Accessors
// =============================================================================

// GetBlock returns the pending block at the given height, if it exists.
func (m *PendingBlocksManager) GetBlock(height int64) (*PendingBlock, bool) {
	m.mtx.RLock()
	defer m.mtx.RUnlock()
	pending, exists := m.blocks[height]
	return pending, exists
}

// GetProposal returns the proposal and part set for a given height and round.
func (m *PendingBlocksManager) GetProposal(height int64, round int32) (*types.Proposal, *types.PartSet, bool) {
	cb, parts, _, has := m.GetAllState(height, round, true)
	if !has {
		return nil, nil, false
	}
	// For catchup blocks, cb may be nil but parts are available.
	if cb == nil {
		return nil, parts.Original(), true
	}
	return &cb.Proposal, parts.Original(), true
}

// GetCurrentProposal returns the current proposal for the current height/round.
func (m *PendingBlocksManager) GetCurrentProposal() (*types.Proposal, *proptypes.CombinedPartSet, bool) {
	m.mtx.RLock()
	defer m.mtx.RUnlock()

	pending, exists := m.blocks[m.height]
	if !exists || pending.CompactBlock == nil {
		return nil, nil, false
	}
	return &pending.CompactBlock.Proposal, pending.Parts, true
}

// GetCurrentCompactBlock returns the current compact block.
func (m *PendingBlocksManager) GetCurrentCompactBlock() (*proptypes.CompactBlock, *proptypes.CombinedPartSet, bool) {
	m.mtx.RLock()
	defer m.mtx.RUnlock()

	pending, exists := m.blocks[m.height]
	if !exists || pending.CompactBlock == nil {
		return nil, nil, false
	}
	return pending.CompactBlock, pending.Parts, true
}

// GetAllState returns the full state for a height/round.
// This also checks the block store for committed blocks.
func (m *PendingBlocksManager) GetAllState(height int64, round int32, catchup bool) (*proptypes.CompactBlock, *proptypes.CombinedPartSet, *bits.BitArray, bool) {
	m.mtx.RLock()
	defer m.mtx.RUnlock()

	if !catchup && !m.relevantLocked(height, round) {
		return nil, nil, nil, false
	}

	// Check pending blocks.
	if pending, exists := m.blocks[height]; exists {
		if pending.Round == round || round < -1 {
			return pending.CompactBlock, pending.Parts, pending.MaxRequests, true
		}
	}

	// Check store for committed blocks.
	if height < m.height && m.store != nil {
		if meta := m.store.LoadBlockMeta(height); meta != nil {
			parts, _, err := m.store.LoadPartSet(height)
			if err != nil {
				return nil, nil, nil, false
			}
			cparts := proptypes.NewCombinedPartSetFromOriginal(parts, false)
			return nil, cparts, cparts.BitArray(), true
		}
	}

	return nil, nil, nil, false
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

// GetCurrentProposalPartsCount returns the current proposal parts count.
func (m *PendingBlocksManager) GetCurrentProposalPartsCount() int64 {
	return m.currentProposalPartsCount.Load()
}

// Relevant checks if a height/round is currently actionable.
func (m *PendingBlocksManager) Relevant(height int64, round int32) bool {
	m.mtx.RLock()
	defer m.mtx.RUnlock()
	return m.relevantLocked(height, round)
}

func (m *PendingBlocksManager) relevantLocked(height int64, round int32) bool {
	if height < m.height {
		return false
	}
	if height == m.height && round < m.round {
		return false
	}
	return true
}

// =============================================================================
// Height/Round Management
// =============================================================================

// SetHeightAndRound updates the current consensus height and round.
func (m *PendingBlocksManager) SetHeightAndRound(height int64, round int32) {
	m.mtx.Lock()
	defer m.mtx.Unlock()
	m.height = height
	m.round = round
}

// GetHeight returns the current height.
func (m *PendingBlocksManager) GetHeight() int64 {
	m.mtx.RLock()
	defer m.mtx.RUnlock()
	return m.height
}

// GetRound returns the current round.
func (m *PendingBlocksManager) GetRound() int32 {
	m.mtx.RLock()
	defer m.mtx.RUnlock()
	return m.round
}

// Store returns the block store.
func (m *PendingBlocksManager) Store() *store.BlockStore {
	return m.store
}

// =============================================================================
// Cleanup
// =============================================================================

// DeleteHeight removes a block from tracking.
func (m *PendingBlocksManager) DeleteHeight(height int64) {
	m.mtx.Lock()
	defer m.mtx.Unlock()
	m.evictBlockLocked(height)
}

// Prune removes all blocks at or below the given height.
func (m *PendingBlocksManager) Prune(committedHeight int64) {
	m.mtx.Lock()
	defer m.mtx.Unlock()

	for height := range m.blocks {
		if height <= committedHeight {
			m.evictBlockLocked(height)
		}
	}
	m.height = committedHeight
}

// =============================================================================
// Internal Helpers
// =============================================================================

func (m *PendingBlocksManager) hasCapacityLocked(cb *proptypes.CompactBlock) bool {
	if len(m.blocks) >= m.config.MaxConcurrent {
		return false
	}
	if cb != nil {
		estimated := m.estimateMemory(cb)
		// Allow adding a single block even if it exceeds the budget (when manager is empty).
		// This ensures the proposer can always propose their own block.
		if m.currentMemory+estimated > m.config.MemoryBudget && len(m.blocks) > 0 {
			return false
		}
	}
	return true
}

func (m *PendingBlocksManager) estimateMemory(cb *proptypes.CompactBlock) int64 {
	if cb == nil {
		return 0
	}
	partSize := int64(types.BlockPartSizeBytes)
	totalParts := int64(cb.Proposal.BlockID.PartSetHeader.Total * 2)
	return totalParts * partSize
}

func (m *PendingBlocksManager) insertHeightLocked(height int64) {
	i := 0
	for i < len(m.heights) && m.heights[i] < height {
		i++
	}
	m.heights = append(m.heights, 0)
	copy(m.heights[i+1:], m.heights[i:])
	m.heights[i] = height
}

func (m *PendingBlocksManager) evictBlockLocked(height int64) {
	pending, exists := m.blocks[height]
	if !exists {
		return
	}

	if pending.CompactBlock != nil {
		m.currentMemory -= m.estimateMemory(pending.CompactBlock)
	}

	delete(m.blocks, height)

	for i, h := range m.heights {
		if h == height {
			m.heights = append(m.heights[:i], m.heights[i+1:]...)
			break
		}
	}
}

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

	return 1.0 / float64(int(1)<<idx)
}

// =============================================================================
// Request Tracking (for peer request limits)
// =============================================================================

// CountRequests returns the peers that have requested a specific part.
func (m *PendingBlocksManager) CountRequests(height int64, round int32, partIndex int) []string {
	// This is tracked at the peer level, not here.
	// Keeping interface for compatibility.
	return nil
}
