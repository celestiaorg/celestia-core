package propagation

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/cometbft/cometbft/crypto/merkle"
	"github.com/cometbft/cometbft/crypto/tmhash"

	proptypes "github.com/cometbft/cometbft/consensus/propagation/types"
	"github.com/cometbft/cometbft/libs/bits"
	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/store"
	cmttypes "github.com/cometbft/cometbft/types"
)

// HeaderSyncReader is the interface for reading verified headers from headersync.
// This allows the PendingBlocksManager to fetch headers on-demand.
type HeaderSyncReader interface {
	// GetVerifiedHeader returns the header and BlockID for a given height if verified.
	GetVerifiedHeader(height int64) (*cmttypes.Header, *cmttypes.BlockID, bool)
	// GetCommit returns the commit for a given height if available.
	GetCommit(height int64) *cmttypes.Commit
	// Height returns the current highest verified header height.
	Height() int64
	// MaxPeerHeight returns the highest peer height seen.
	MaxPeerHeight() int64
	// IsCaughtUp returns whether headersync is caught up to peers.
	IsCaughtUp() bool
}

const (
	defaultMaxConcurrent = 500
	defaultMemoryBudget  = int64(12 * (1 << 30)) // 12GiB
)

type BlockSource int

const (
	SourceCompactBlock BlockSource = iota // Live proposal from gossip
	SourceHeaderSync                      // Verified header from headersync
	SourceCommitment                      // PartSetHeader from consensus commit
)

type PendingBlockState int

const (
	BlockStateActive   PendingBlockState = iota // Downloading parts
	BlockStateComplete                          // All original parts received
)

type PendingBlock struct {
	Height         int64
	Round          int32
	Source         BlockSource                // How this block was FIRST added
	BlockID        cmttypes.BlockID           // From verified header (may be partial if from commitment)
	Parts          *proptypes.CombinedPartSet // Original + parity parts
	CompactBlock   *proptypes.CompactBlock    // Nil until/unless CompactBlock arrives
	MaxRequests    *bits.BitArray             // Per-part request tracking
	HeaderVerified bool                       // Whether headersync verified header
	HasCommitment  bool                       // Whether consensus provided commitment
	Commit         *cmttypes.Commit           // From headersync (for blocksync application)
	CreatedAt      time.Time
	State          PendingBlockState
	allocatedBytes int64 // Memory tracking
}

// CanUseProofCache returns true if we have a CompactBlock with proof cache.
func (pb *PendingBlock) CanUseProofCache() bool {
	return pb.CompactBlock != nil && len(pb.CompactBlock.PartsHashes) > 0
}

// NeedsInlineProofs returns true if parts must include Merkle proofs.
func (pb *PendingBlock) NeedsInlineProofs() bool {
	return !pb.CanUseProofCache()
}

type PendingBlocksConfig struct {
	MaxConcurrent int   // Max blocks tracked (default: 500)
	MemoryBudget  int64 // Max memory in bytes (default: 12GiB)
}

type PendingBlocksManager struct {
	mtx        sync.RWMutex
	logger     log.Logger
	blockStore *store.BlockStore

	// Sorted by height (ascending) for priority
	heights []int64
	blocks  map[int64]*PendingBlock // height -> block

	// Memory management
	config        PendingBlocksConfig
	currentMemory int64

	// Output channel for completed blocks
	completedBlocks chan *CompletedBlock

	// Headersync integration
	hsReader            HeaderSyncReader
	lastCommittedHeight int64
}

type CompletedBlock struct {
	Height  int64
	Round   int32
	Parts   *cmttypes.PartSet
	BlockID cmttypes.BlockID
	Commit  *cmttypes.Commit // From headersync (for blocksync application)
}

// NewPendingBlocksManager constructs a manager with sane defaults for configuration
// and initialized internal state.
func NewPendingBlocksManager(logger log.Logger, blockStore *store.BlockStore, cfg PendingBlocksConfig) *PendingBlocksManager {
	if logger == nil {
		logger = log.NewNopLogger()
	}

	cfg = applyPendingBlocksDefaults(cfg)

	return &PendingBlocksManager{
		logger:          logger,
		blockStore:      blockStore,
		heights:         []int64{},
		blocks:          make(map[int64]*PendingBlock),
		config:          cfg,
		completedBlocks: make(chan *CompletedBlock, cfg.MaxConcurrent), // Buffered to avoid blocking
	}
}

func applyPendingBlocksDefaults(cfg PendingBlocksConfig) PendingBlocksConfig {
	if cfg.MaxConcurrent == 0 {
		cfg.MaxConcurrent = defaultMaxConcurrent
	}
	if cfg.MemoryBudget == 0 {
		cfg.MemoryBudget = defaultMemoryBudget
	}
	return cfg
}

// addBlock inserts the pending block into the manager. Caller must hold mtx.
func (m *PendingBlocksManager) addBlock(pb *PendingBlock) {
	fmt.Println("adding pending block", pb.Height, pb.Source)
	if _, exists := m.blocks[pb.Height]; !exists {
		m.insertHeight(pb.Height)
	}

	m.blocks[pb.Height] = pb
}

// insertHeight maintains the ascending order of the tracked heights slice.
func (m *PendingBlocksManager) insertHeight(height int64) {
	idx := sort.Search(len(m.heights), func(i int) bool { return m.heights[i] >= height })
	// height already present
	if idx < len(m.heights) && m.heights[idx] == height {
		return
	}

	m.heights = append(m.heights, 0)
	copy(m.heights[idx+1:], m.heights[idx:])
	m.heights[idx] = height
}

// estimateBlockMemory estimates memory usage for a block based on its PartSetHeader.
func (m *PendingBlocksManager) estimateBlockMemory(psh *cmttypes.PartSetHeader) int64 {
	// partSize * totalParts * 2 (for parity)
	return int64(cmttypes.BlockPartSizeBytes) * int64(psh.Total) * 2
}

// canAddBlock checks if there is capacity to add a block with the given memory requirement.
// Caller must hold mtx.
func (m *PendingBlocksManager) canAddBlock(memoryNeeded int64) bool {
	return m.currentMemory+memoryNeeded <= m.config.MemoryBudget &&
		len(m.blocks) < m.config.MaxConcurrent
}

// AddProposal adds a CompactBlock from live gossip. It can either create a new
// PendingBlock or attach to an existing one if header/commitment arrived first.
func (m *PendingBlocksManager) AddProposal(cb *proptypes.CompactBlock) (bool, error) {
	fmt.Println("trying to adding proposal", cb.Proposal.Height)
	m.mtx.Lock()
	defer m.mtx.Unlock()

	height := cb.Proposal.Height

	existing := m.blocks[height]
	if existing != nil {
		fmt.Println("didn't exist yet")
		// ATTACH to existing block (header or commitment arrived first)
		if existing.CompactBlock != nil {
			return false, nil // Already have CompactBlock, ignore duplicate
		}

		// Validate BlockID matches if we have a verified header
		if existing.HeaderVerified {
			if !existing.BlockID.Equals(cb.Proposal.BlockID) {
				return false, fmt.Errorf("CompactBlock BlockID mismatch with verified header at height %d", height)
			}
		}

		// Validate PartSetHeader matches if we have a commitment
		if existing.HasCommitment {
			if !existing.BlockID.PartSetHeader.Equals(cb.Proposal.BlockID.PartSetHeader) {
				return false, fmt.Errorf("CompactBlock PSH mismatch with commitment at height %d", height)
			}
		}

		// Attach CompactBlock - now we have proof cache!
		existing.CompactBlock = cb
		return true, nil
	}

	// NEW block - check capacity/memory limits
	memNeeded := m.estimateBlockMemory(&cb.Proposal.BlockID.PartSetHeader)
	if !m.canAddBlock(memNeeded) {
		return false, fmt.Errorf("capacity exceeded for height %d", height)
	}

	// Create new PendingBlock
	pb := &PendingBlock{
		Height:         height,
		Round:          cb.Proposal.Round,
		Source:         SourceCompactBlock,
		BlockID:        cb.Proposal.BlockID,
		CompactBlock:   cb,
		Parts:          proptypes.NewCombinedSetFromCompactBlock(cb),
		MaxRequests:    bits.NewBitArray(int(cb.Proposal.BlockID.PartSetHeader.Total * 2)),
		CreatedAt:      time.Now(),
		State:          BlockStateActive,
		allocatedBytes: memNeeded,
	}
	m.addBlock(pb)
	m.currentMemory += memNeeded
	return true, nil
}

// AddFromHeader adds a verified header from headersync. It can create a new block
// or upgrade an existing commitment-created block.
func (m *PendingBlocksManager) AddFromHeader(header *cmttypes.Header, blockID cmttypes.BlockID, commit *cmttypes.Commit) (bool, error) {
	m.mtx.Lock()
	defer m.mtx.Unlock()

	height := header.Height

	existing := m.blocks[height]
	if existing != nil {
		if existing.HeaderVerified {
			return false, nil // Already have verified header
		}

		// UPGRADE existing block (was created from commitment)
		// Validate PSH matches
		if existing.HasCommitment {
			if !existing.BlockID.PartSetHeader.Equals(blockID.PartSetHeader) {
				return false, fmt.Errorf("header PSH mismatch with commitment at height %d", height)
			}
		}

		// Validate against CompactBlock if present
		if existing.CompactBlock != nil {
			if !existing.CompactBlock.Proposal.BlockID.Equals(blockID) {
				return false, fmt.Errorf("header BlockID mismatch with CompactBlock at height %d", height)
			}
		}

		existing.BlockID = blockID
		existing.HeaderVerified = true
		existing.Commit = commit
		return true, nil
	}

	// NEW block - capacity was checked before we fetched this header
	// (see tryFillCapacity in the plan). Just add it.
	psh := blockID.PartSetHeader
	memNeeded := m.estimateBlockMemory(&psh)

	pb := &PendingBlock{
		Height:         height,
		Round:          0, // Round comes from commit or CompactBlock later
		Source:         SourceHeaderSync,
		BlockID:        blockID,
		HeaderVerified: true,
		Commit:         commit,
		Parts:          proptypes.NewCombinedPartSetFromOriginal(cmttypes.NewPartSetFromHeader(psh, cmttypes.BlockPartSizeBytes), false),
		MaxRequests:    bits.NewBitArray(int(psh.Total * 2)),
		CreatedAt:      time.Now(),
		State:          BlockStateActive,
		allocatedBytes: memNeeded,
	}
	m.addBlock(pb)
	m.currentMemory += memNeeded
	return true, nil
}

// AddFromCommitment adds a block from consensus when +2/3 precommits are received
// but block data is lacking. Creates a minimal block that can be upgraded later.
// This ALWAYS succeeds - commitments bypass capacity limits.
func (m *PendingBlocksManager) AddFromCommitment(height int64, round int32, psh *cmttypes.PartSetHeader) bool {
	m.mtx.Lock()
	defer m.mtx.Unlock()

	existing := m.blocks[height]
	if existing != nil {
		// Already tracking this height - just mark that we have commitment
		if !existing.HasCommitment {
			existing.HasCommitment = true
			// Validate PSH matches if we have data
			if existing.BlockID.PartSetHeader.Total > 0 {
				if !existing.BlockID.PartSetHeader.Equals(*psh) {
					m.logger.Error("commitment PSH mismatch", "height", height,
						"existing", existing.BlockID.PartSetHeader.Hash,
						"commitment", psh.Hash)
					// Don't return error - commitment is authoritative
				}
			}
			existing.BlockID.PartSetHeader = *psh
		}
		return false // Already existed
	}

	// NEW block from commitment
	// BYPASS capacity limits - commitments MUST be accepted
	memNeeded := m.estimateBlockMemory(psh)

	pb := &PendingBlock{
		Height:         height,
		Round:          round,
		Source:         SourceCommitment,
		BlockID:        cmttypes.BlockID{PartSetHeader: *psh}, // Only PSH known
		HasCommitment:  true,
		Parts:          proptypes.NewCombinedPartSetFromOriginal(cmttypes.NewPartSetFromHeader(*psh, cmttypes.BlockPartSizeBytes), true),
		MaxRequests:    bits.NewBitArray(int(psh.Total * 2)),
		CreatedAt:      time.Now(),
		State:          BlockStateActive,
		allocatedBytes: memNeeded,
	}
	m.addBlock(pb) // Bypasses capacity check
	m.currentMemory += memNeeded
	return true
}

// MissingPartsInfo contains information about missing parts for a pending block.
type MissingPartsInfo struct {
	Height        int64
	Round         int32
	Missing       *bits.BitArray
	Total         uint32
	NeedsProofs   bool // Whether requested parts must include inline Merkle proofs
	HasCommitment bool // Whether this block has a consensus commitment (prioritize)
}

// HandlePart receives a part for a pending block, verifies it, and adds it to the CombinedPartSet.
// Returns (added, complete, error) where:
//   - added: true if the part was successfully added
//   - complete: true if this part completed the original part set
//   - error: any validation error
func (m *PendingBlocksManager) HandlePart(height int64, round int32, part *proptypes.RecoveryPart, proof *merkle.Proof) (bool, bool, error) {
	m.mtx.Lock()
	defer m.mtx.Unlock()

	pb := m.blocks[height]
	if pb == nil {
		// Unknown height - could be already pruned or never tracked
		return false, false, nil
	}

	// If block is already complete, ignore the part
	if pb.State == BlockStateComplete {
		return false, false, nil
	}

	// Check if we already have this part
	if pb.Parts.HasPart(int(part.Index)) {
		return false, false, nil
	}

	// Verify the part using either proof cache (from CompactBlock) or inline proof
	var verifiedProof merkle.Proof
	if pb.CanUseProofCache() {
		// Use proof from CompactBlock's proof cache
		cachedProof := pb.CompactBlock.GetProof(part.Index)
		if cachedProof == nil {
			return false, false, fmt.Errorf("no cached proof for part %d at height %d", part.Index, height)
		}
		verifiedProof = *cachedProof
	} else {
		// Must have inline proof
		if proof == nil {
			return false, false, fmt.Errorf("missing inline proof for part %d at height %d (no CompactBlock)", part.Index, height)
		}
		if len(proof.LeafHash) != tmhash.Size {
			return false, false, fmt.Errorf("invalid proof leaf hash size for part %d at height %d", part.Index, height)
		}
		verifiedProof = *proof
	}

	// Add part to CombinedPartSet (AddPart verifies the proof internally)
	added, err := pb.Parts.AddPart(part, verifiedProof)
	if err != nil {
		return false, false, fmt.Errorf("failed to add part %d at height %d: %w", part.Index, height, err)
	}

	if !added {
		return false, false, nil
	}

	// Check if original parts are now complete
	complete := pb.Parts.IsComplete()
	if complete {
		pb.State = BlockStateComplete
		// Send to completedBlocks channel (non-blocking to avoid deadlock)
		// The caller is responsible for consuming this channel
		select {
		case m.completedBlocks <- &CompletedBlock{
			Height:  pb.Height,
			Round:   pb.Round,
			Parts:   pb.Parts.Original(),
			BlockID: pb.BlockID,
			Commit:  pb.Commit,
		}:
		default:
			m.logger.Error("completedBlocks channel full, dropping completion", "height", height)
		}
	}

	return true, complete, nil
}

// GetMissingParts returns information about missing parts for up to maxBlocks pending blocks.
// Results are ordered by height (lowest first) to prioritize older blocks.
// Only returns blocks in Active state.
func (m *PendingBlocksManager) GetMissingParts(maxBlocks int) []*MissingPartsInfo {
	m.mtx.RLock()
	defer m.mtx.RUnlock()

	result := make([]*MissingPartsInfo, 0, maxBlocks)

	for _, height := range m.heights {
		if len(result) >= maxBlocks {
			break
		}

		pb := m.blocks[height]
		if pb == nil || pb.State != BlockStateActive {
			continue
		}

		// Get missing parts bitmap (inverted from what we have)
		// Total map includes both original and parity parts
		haveBits := pb.Parts.BitArray()
		missingBits := haveBits.Not()

		// Check if there are any missing parts
		if missingBits.IsEmpty() {
			continue
		}

		result = append(result, &MissingPartsInfo{
			Height:        pb.Height,
			Round:         pb.Round,
			Missing:       missingBits.Copy(),
			Total:         pb.Parts.Total(),
			NeedsProofs:   pb.NeedsInlineProofs(),
			HasCommitment: pb.HasCommitment,
		})
	}

	return result
}

// GetBlock returns the pending block at the given height, or nil if not found.
func (m *PendingBlocksManager) GetBlock(height int64) *PendingBlock {
	m.mtx.RLock()
	defer m.mtx.RUnlock()
	return m.blocks[height]
}

// HasHeight returns true if the manager is tracking the given height.
func (m *PendingBlocksManager) HasHeight(height int64) bool {
	m.mtx.RLock()
	defer m.mtx.RUnlock()
	_, exists := m.blocks[height]
	return exists
}

// CompletedBlocksChan returns the channel for receiving completed blocks.
func (m *PendingBlocksManager) CompletedBlocksChan() <-chan *CompletedBlock {
	return m.completedBlocks
}

// HasCapacity returns true if we can add at least one more block.
// Called by tryFillCapacity BEFORE fetching headers.
func (m *PendingBlocksManager) HasCapacity() bool {
	m.mtx.RLock()
	defer m.mtx.RUnlock()
	// Conservative estimate - assume average block size (~100 parts)
	avgBlockMemory := int64(cmttypes.BlockPartSizeBytes) * 100 * 2
	return m.currentMemory+avgBlockMemory <= m.config.MemoryBudget &&
		len(m.blocks) < m.config.MaxConcurrent
}

// AvailableCapacity returns how many more blocks we can track.
// Used to determine how many headers to fetch.
func (m *PendingBlocksManager) AvailableCapacity() int {
	m.mtx.RLock()
	defer m.mtx.RUnlock()
	return m.config.MaxConcurrent - len(m.blocks)
}

// CurrentMemory returns the current memory usage in bytes.
func (m *PendingBlocksManager) CurrentMemory() int64 {
	m.mtx.RLock()
	defer m.mtx.RUnlock()
	return m.currentMemory
}

// BlockCount returns the number of blocks currently being tracked.
func (m *PendingBlocksManager) BlockCount() int {
	m.mtx.RLock()
	defer m.mtx.RUnlock()
	return len(m.blocks)
}

// Prune removes all blocks with height <= committedHeight.
// This should be called after a block has been committed.
func (m *PendingBlocksManager) Prune(committedHeight int64) {
	m.mtx.Lock()
	defer m.mtx.Unlock()

	// Find the cutoff index in the sorted heights slice
	cutoffIdx := 0
	for i, h := range m.heights {
		if h > committedHeight {
			cutoffIdx = i
			break
		}
		cutoffIdx = i + 1
	}

	// Remove blocks and update memory
	for i := 0; i < cutoffIdx; i++ {
		height := m.heights[i]
		if pb, exists := m.blocks[height]; exists {
			m.currentMemory -= pb.allocatedBytes
			delete(m.blocks, height)
		}
	}

	// Trim the heights slice
	if cutoffIdx > 0 {
		m.heights = m.heights[cutoffIdx:]
	}
}

// LowestHeight returns the lowest height being tracked, or 0 if none.
func (m *PendingBlocksManager) LowestHeight() int64 {
	m.mtx.RLock()
	defer m.mtx.RUnlock()
	if len(m.heights) == 0 {
		return 0
	}
	return m.heights[0]
}

// HighestHeight returns the highest height being tracked, or 0 if none.
func (m *PendingBlocksManager) HighestHeight() int64 {
	m.mtx.RLock()
	defer m.mtx.RUnlock()
	if len(m.heights) == 0 {
		return 0
	}
	return m.heights[len(m.heights)-1]
}

// SetHeaderSyncReader sets the HeaderSyncReader for on-demand header fetching.
// This should be called during reactor setup before any headers are needed.
func (m *PendingBlocksManager) SetHeaderSyncReader(reader HeaderSyncReader) {
	m.mtx.Lock()
	defer m.mtx.Unlock()
	m.hsReader = reader
}

// SetLastCommittedHeight updates the last committed height.
// This is used to determine the lowest needed height for fetching.
func (m *PendingBlocksManager) SetLastCommittedHeight(height int64) {
	m.mtx.Lock()
	defer m.mtx.Unlock()
	m.lastCommittedHeight = height
}

// GetLastCommittedHeight returns the last committed height.
func (m *PendingBlocksManager) GetLastCommittedHeight() int64 {
	m.mtx.RLock()
	defer m.mtx.RUnlock()
	return m.lastCommittedHeight
}

// lowestNeededHeight returns the lowest height we should be fetching.
// This is typically lastCommittedHeight + 1.
// Caller must hold at least a read lock on mtx.
func (m *PendingBlocksManager) lowestNeededHeightLocked() int64 {
	if len(m.heights) == 0 {
		return m.lastCommittedHeight + 1
	}
	// If we have pending blocks, start from lowest pending
	// (in case there are gaps below it)
	lowestPending := m.heights[0]
	lowestNeeded := m.lastCommittedHeight + 1
	if lowestPending < lowestNeeded {
		return lowestPending
	}
	return lowestNeeded
}

// LowestNeededHeight returns the lowest height we should be fetching headers for.
func (m *PendingBlocksManager) LowestNeededHeight() int64 {
	m.mtx.RLock()
	defer m.mtx.RUnlock()
	return m.lowestNeededHeightLocked()
}

// TryFillCapacity fetches headers for consecutive heights starting from
// the lowest height we need, up to available capacity.
// This is the main entry point - always fills from lowest height first.
//
// KEY: Capacity is checked HERE, before fetching. We never request
// headers we can't handle.
//
// Returns the number of headers successfully fetched and added/upgraded.
func (m *PendingBlocksManager) TryFillCapacity() int {
	fmt.Println("try fill capacity")
	m.mtx.Lock()
	defer m.mtx.Unlock()

	if m.hsReader == nil {
		return 0
	}

	hsHeight := m.hsReader.Height() // Highest verified header available
	startHeight := m.lowestNeededHeightLocked()

	fetched := 0
	for height := startHeight; height <= hsHeight; height++ {
		// Check if we already have this height with a verified header
		existing := m.blocks[height]
		if existing != nil && existing.HeaderVerified {
			continue // Already have verified header, skip
		}

		// If no existing block, check capacity before adding new one
		if existing == nil {
			avgBlockMemory := int64(cmttypes.BlockPartSizeBytes) * 100 * 2
			if m.currentMemory+avgBlockMemory > m.config.MemoryBudget ||
				len(m.blocks) >= m.config.MaxConcurrent {
				break // At capacity, don't request more headers
			}
		}

		// Fetch and add/upgrade this header
		if m.fetchAndAddHeaderLocked(height) {
			fetched++
		}
	}

	return fetched
}

// fetchAndAddHeaderLocked fetches a header from headersync and adds it.
// Only called when we've already confirmed we have capacity.
// Caller must hold mtx (write lock).
// Returns true if a header was successfully added.
func (m *PendingBlocksManager) fetchAndAddHeaderLocked(height int64) bool {
	fmt.Println("fetch and add header locked")
	if m.hsReader == nil {
		return false
	}

	_, blockID, ok := m.hsReader.GetVerifiedHeader(height)
	if !ok {
		return false // Header not yet verified by headersync
	}

	commit := m.hsReader.GetCommit(height)
	if commit == nil {
		return false // Commit not available yet
	}

	// Check if block already exists (may have been added via commitment or compact block)
	existing := m.blocks[height]
	if existing != nil {
		if existing.HeaderVerified {
			return false // Already have verified header
		}

		// UPGRADE existing block (was created from commitment or compact block)
		// Validate PSH matches
		if existing.HasCommitment {
			if !existing.BlockID.PartSetHeader.Equals(blockID.PartSetHeader) {
				m.logger.Error("header PSH mismatch with commitment during fetch",
					"height", height,
					"existing", existing.BlockID.PartSetHeader.Hash,
					"header", blockID.PartSetHeader.Hash)
				return false
			}
		}

		// Validate against CompactBlock if present
		if existing.CompactBlock != nil {
			if !existing.CompactBlock.Proposal.BlockID.Equals(*blockID) {
				m.logger.Error("header BlockID mismatch with CompactBlock during fetch",
					"height", height)
				return false
			}
		}

		existing.BlockID = *blockID
		existing.HeaderVerified = true
		existing.Commit = commit
		return true
	}

	// NEW block from header
	psh := blockID.PartSetHeader
	memNeeded := m.estimateBlockMemory(&psh)

	pb := &PendingBlock{
		Height:         height,
		Round:          0, // Round comes from commit or CompactBlock later
		Source:         SourceHeaderSync,
		BlockID:        *blockID,
		HeaderVerified: true,
		Commit:         commit,
		Parts:          proptypes.NewCombinedPartSetFromOriginal(cmttypes.NewPartSetFromHeader(psh, cmttypes.BlockPartSizeBytes), false),
		MaxRequests:    bits.NewBitArray(int(psh.Total * 2)),
		CreatedAt:      time.Now(),
		State:          BlockStateActive,
		allocatedBytes: memNeeded,
	}
	m.addBlock(pb)
	m.currentMemory += memNeeded
	return true
}

// TryFetchHeader attempts to fetch a single header at the given height.
// This is useful for immediate header fetch when a commitment arrives.
// Returns true if the header was successfully fetched and added/upgraded.
func (m *PendingBlocksManager) TryFetchHeader(height int64) bool {
	m.mtx.Lock()
	defer m.mtx.Unlock()
	return m.fetchAndAddHeaderLocked(height)
}

// OnBlockComplete should be called when a block completes.
// It triggers a capacity refill from headersync.
func (m *PendingBlocksManager) OnBlockComplete(height int64) {
	// Space freed up - try to fill with next headers starting from lowest needed
	m.TryFillCapacity()
}

// BlockDeliveryManager ensures that completed blocks are delivered to consensus
// in strictly increasing height order. This is critical for blocksync mode where
// blocks must be applied sequentially.
//
// Live consensus parts continue to flow through partChan immediately (unchanged).
// This manager only handles blocksync (historical) blocks.
type BlockDeliveryManager struct {
	mtx           sync.Mutex
	pendingBlocks *PendingBlocksManager
	nextHeight    int64                     // Next blocksync height to deliver
	readyBlocks   map[int64]*CompletedBlock // Completed blocks waiting for earlier heights
	blockChan     chan *CompletedBlock      // Output to consensus
	logger        log.Logger
	running       bool
	stopCh        chan struct{}
}

// NewBlockDeliveryManager creates a new BlockDeliveryManager.
// startHeight is the first height that should be delivered (typically lastCommittedHeight + 1).
func NewBlockDeliveryManager(pendingBlocks *PendingBlocksManager, startHeight int64, logger log.Logger) *BlockDeliveryManager {
	if logger == nil {
		logger = log.NewNopLogger()
	}
	return &BlockDeliveryManager{
		pendingBlocks: pendingBlocks,
		nextHeight:    startHeight,
		readyBlocks:   make(map[int64]*CompletedBlock),
		blockChan:     make(chan *CompletedBlock, 10), // Small buffer to avoid blocking
		logger:        logger,
		stopCh:        make(chan struct{}),
	}
}

// Start begins the block delivery routine.
func (d *BlockDeliveryManager) Start() {
	d.mtx.Lock()
	if d.running {
		d.mtx.Unlock()
		return
	}
	d.running = true
	d.mtx.Unlock()

	go d.run()
}

// Stop stops the block delivery routine.
func (d *BlockDeliveryManager) Stop() {
	d.mtx.Lock()
	if !d.running {
		d.mtx.Unlock()
		return
	}
	d.running = false
	d.mtx.Unlock()

	close(d.stopCh)
}

// run is the main delivery loop that processes completed blocks and delivers
// them in strictly increasing height order.
func (d *BlockDeliveryManager) run() {
	completedChan := d.pendingBlocks.CompletedBlocksChan()

	for {
		select {
		case <-d.stopCh:
			return
		case completed, ok := <-completedChan:
			if !ok {
				return
			}
			d.handleCompletion(completed)
		}
	}
}

// handleCompletion processes a completed block, buffering it if necessary
// and delivering all consecutive ready blocks in order.
func (d *BlockDeliveryManager) handleCompletion(completed *CompletedBlock) {
	d.mtx.Lock()
	defer d.mtx.Unlock()

	// Ignore stale blocks (already delivered or before our start point)
	if completed.Height < d.nextHeight {
		d.logger.Debug("ignoring stale completed block",
			"height", completed.Height,
			"nextHeight", d.nextHeight)
		return
	}

	// Ignore duplicate completions
	if _, exists := d.readyBlocks[completed.Height]; exists {
		d.logger.Debug("ignoring duplicate completed block",
			"height", completed.Height)
		return
	}

	// Buffer the completed block
	d.readyBlocks[completed.Height] = completed

	// Deliver all consecutive ready blocks IN ORDER
	d.deliverReadyBlocksLocked()
}

// deliverReadyBlocksLocked delivers all consecutive ready blocks starting from nextHeight.
// Caller must hold mtx.
func (d *BlockDeliveryManager) deliverReadyBlocksLocked() {
	for {
		block, ok := d.readyBlocks[d.nextHeight]
		if !ok {
			break
		}
		delete(d.readyBlocks, d.nextHeight)

		// Deliver to consensus (this may block if consensus is slow)
		// We release the lock during delivery to avoid deadlock
		d.mtx.Unlock()
		select {
		case <-d.stopCh:
			d.mtx.Lock()
			return
		case d.blockChan <- block:
			d.logger.Debug("delivered block to consensus", "height", block.Height)
		}
		d.mtx.Lock()

		d.nextHeight++
	}
}

// BlockChan returns the channel for receiving ordered completed blocks.
// Blocks on this channel are guaranteed to arrive in strictly increasing height order.
func (d *BlockDeliveryManager) BlockChan() <-chan *CompletedBlock {
	return d.blockChan
}

// NextHeight returns the next height that will be delivered.
func (d *BlockDeliveryManager) NextHeight() int64 {
	d.mtx.Lock()
	defer d.mtx.Unlock()
	return d.nextHeight
}

// SetNextHeight updates the next height to deliver.
// This should only be called during initialization or after a state reset.
func (d *BlockDeliveryManager) SetNextHeight(height int64) {
	d.mtx.Lock()
	defer d.mtx.Unlock()
	d.nextHeight = height
	// Clear any buffered blocks that are now stale
	for h := range d.readyBlocks {
		if h < height {
			delete(d.readyBlocks, h)
		}
	}
	// Try to deliver any ready blocks at or after the new height
	d.deliverReadyBlocksLocked()
}

// PendingCount returns the number of blocks buffered waiting for earlier heights.
func (d *BlockDeliveryManager) PendingCount() int {
	d.mtx.Lock()
	defer d.mtx.Unlock()
	return len(d.readyBlocks)
}

// SetLogger sets the logger for the BlockDeliveryManager.
func (d *BlockDeliveryManager) SetLogger(logger log.Logger) {
	d.mtx.Lock()
	defer d.mtx.Unlock()
	d.logger = logger
}

// SetLogger sets the logger for the PendingBlocksManager.
func (m *PendingBlocksManager) SetLogger(logger log.Logger) {
	m.mtx.Lock()
	defer m.mtx.Unlock()
	m.logger = logger
}
