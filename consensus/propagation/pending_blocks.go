package propagation

import (
	"fmt"
	"sort"
	"sync"
	"time"

	proptypes "github.com/cometbft/cometbft/consensus/propagation/types"
	"github.com/cometbft/cometbft/libs/bits"
	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/store"
	cmttypes "github.com/cometbft/cometbft/types"
)

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
}

type CompletedBlock struct {
	Height  int64
	Round   int32
	Parts   *cmttypes.PartSet
	BlockID cmttypes.BlockID
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
		completedBlocks: make(chan *CompletedBlock),
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
	m.mtx.Lock()
	defer m.mtx.Unlock()

	height := cb.Proposal.Height

	existing := m.blocks[height]
	if existing != nil {
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
