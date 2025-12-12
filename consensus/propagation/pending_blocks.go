package propagation

import (
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
