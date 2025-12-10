# ADR 014: Pipelined Block Propagation with Header-First Verification

## Changelog

- 2025-12-10: Final refactor - clean event-driven architecture with BlockSource tracking
- 2025-12-10: Updated to reflect implementation - unified PendingBlocksManager replaces ProposalCache
- 2025-12-09: Initial draft

## Context

### Current Architecture Limitations

The propagation reactor currently operates on a single "current" block at a time. When a new compact block arrives for height H+1 while the node is still downloading parts for height H, one of two things happens:

1. The new compact block is ignored until height H completes
2. The reactor switches to H+1, potentially abandoning incomplete work on H

Neither is optimal. The ADR-013 catchup coordinator attempted to address this by creating a separate path for "catchup" blocks, but the integration is awkward:
- There's no seamless transition between "live" and "catchup" modes
- The ProposalCache holds one block at a time, creating artificial serialization
- Parts arriving for height H+1 while processing H are dropped or misrouted

### The Opportunity

With headersync providing verified headers ahead of block data, we can:
1. **Verify compact blocks before allocation**: When we receive a compact block for H+1, check if headersync has verified its header
2. **Pipeline block downloads**: Download parts for heights H and H+1 concurrently with proper prioritization
3. **Graceful degradation**: If headersync doesn't have a header yet, fall back to current behavior

The key insight: we're not doing "catchup" vs "live" - we're always doing the same thing: **downloading verified blocks in priority order**.

## Decision

**Refactor the propagation reactor to support concurrent block downloads** using headersync verification as the trust anchor for all blocks, whether "current" or "next".

### Design Principles

1. **No mode switching** - A single unified download path handles all blocks
2. **Priority by height** - Lower heights always get priority (FIFO completion)
3. **Verify before allocate** - Only allocate memory for blocks with verified headers
4. **Bounded concurrency** - Fixed maximum blocks in flight
5. **Event-driven** - Parts flow to the appropriate block based on height

## Architecture

### Block Download Pipeline

```
Verified Headers                    Compact Blocks
(from headersync)                   (from peers)
       │                                  │
       ▼                                  ▼
  ┌─────────────────────────────────────────────────────┐
  │              PendingBlocksManager                    │
  │  ┌──────────────────────────────────────────────┐   │
  │  │  Height H (priority 1) ← oldest, complete first │   │
  │  │  Height H+1 (priority 2)                     │   │
  │  │  Height H+2 (priority 3) ← limit concurrency │   │
  │  └──────────────────────────────────────────────┘   │
  └─────────────────────────────────────────────────────┘
                          │
                          ▼ completed blocks
                  ┌───────────────┐
                  │  Consensus    │
                  │   Reactor     │
                  └───────────────┘
```

### Core Data Structures

```go
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

// PendingBlock represents a block being downloaded.
// Created when either:
// 1. A compact block arrives (live proposal) - SourceCompactBlock
// 2. A verified header arrives from headersync - SourceHeaderSync
// 3. A commitment is received (edge case: consensus learns about committed block before headersync) - SourceCommitment
type PendingBlock struct {
    Height        int64
    Round         int32
    Source        BlockSource  // How this block was added
    BlockID       types.BlockID  // From verified header or commitment

    // Block data - combined original + parity parts
    Parts         *proptypes.CombinedPartSet

    // Compact block metadata (nil for catchup-only blocks)
    CompactBlock  *proptypes.CompactBlock

    // Request tracking for this block
    MaxRequests   *bits.BitArray

    // Whether header has been verified by headersync
    HeaderVerified bool

    // Timing
    CreatedAt     time.Time

    // State
    State         PendingBlockState
}

type PendingBlockState int
const (
    // BlockStateActive - downloading parts
    BlockStateActive PendingBlockState = iota
    // BlockStateComplete - all original parts received
    BlockStateComplete
)

// PendingBlocksManager manages all block downloads - both live and catchup.
// This is the unified replacement for ProposalCache.
type PendingBlocksManager struct {
    mtx           sync.RWMutex
    logger        log.Logger

    // Block store for loading committed blocks
    store         *store.BlockStore

    // Pending blocks indexed by height
    blocks        map[int64]*PendingBlock

    // Heights sorted ascending (lowest = highest priority)
    heights       []int64

    // Current consensus height and round
    height        int64
    round         int32

    // Constraints
    config        PendingBlocksConfig
    currentMemory int64

    // Verified headers from headersync
    headerVerifier HeaderVerifier

    // Output channel for forwarding parts to consensus
    partsChan     chan<- types.PartInfo

    // Callback when a block is added (for triggering catchup)
    onBlockAdded  BlockAddedCallback

    // Current proposal parts count for request limiting
    currentProposalPartsCount atomic.Int64
}

// BlockAddedCallback is called when a new block is added to the manager.
// This allows the reactor to trigger catchup immediately when needed.
type BlockAddedCallback func(height int64, source BlockSource)

// HeaderVerifier provides access to verified headers.
type HeaderVerifier interface {
    // GetVerifiedHeader returns a verified header if available.
    GetVerifiedHeader(height int64) (*types.Header, *types.BlockID, bool)
}

// MissingPartsInfo describes parts needed for a block.
type MissingPartsInfo struct {
    Height         int64
    Round          int32
    TotalParts     uint32
    MissingIndices []int
    Priority       float64
    Catchup        bool  // True if catchup-only block (no parity data)
}
```

### Compact Block Processing Flow

```go
// AddProposal adds a new compact block for the live consensus round.
func (m *PendingBlocksManager) AddProposal(cb *proptypes.CompactBlock) bool {
    m.mtx.Lock()
    defer m.mtx.Unlock()

    height := cb.Proposal.Height
    round := cb.Proposal.Round

    if !m.relevantLocked(height, round) {
        return false
    }

    // Case 1: Already have this height
    if pending, exists := m.blocks[height]; exists {
        if pending.CompactBlock != nil {
            return false // Duplicate
        }
        // We had a header-only entry, attach the compact block
        return m.activateWithCompactBlockLocked(pending, cb)
    }

    // Case 2: Check capacity, evicting lower heights if needed
    if !m.hasCapacityLocked(cb) {
        if !m.evictLowerHeightsLocked(height, cb) {
            return false
        }
    }

    // Create new pending block
    m.height = height
    m.round = round

    parts := proptypes.NewCombinedSetFromCompactBlock(cb)
    pending := &PendingBlock{
        Height:       height,
        Round:        round,
        CompactBlock: cb,
        Parts:        parts,
        MaxRequests:  bits.NewBitArray(int(parts.Total())),
        CreatedAt:    time.Now(),
        State:        BlockStateActive,
    }

    // Check if header is verified
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

    return true
}

// AddCommitment handles the edge case where consensus learns about a committed
// block before headersync verifies the header.
func (m *PendingBlocksManager) AddCommitment(height int64, round int32, psh *types.PartSetHeader) {
    m.mtx.Lock()
    defer m.mtx.Unlock()

    if _, exists := m.blocks[height]; exists {
        return // Already tracking
    }

    if !m.hasCapacityLocked(nil) {
        return
    }

    // Create catchup block from PartSetHeader only
    original := types.NewPartSetFromHeader(*psh, types.BlockPartSizeBytes)
    parts := proptypes.NewCombinedPartSetFromOriginal(original, true) // catchup=true

    pending := &PendingBlock{
        Height:         height,
        Round:          round,
        BlockID:        types.BlockID{PartSetHeader: *psh},
        Parts:          parts,
        MaxRequests:    bits.NewBitArray(int(parts.Total())),
        HeaderVerified: true, // Trusted from commit
        CreatedAt:      time.Now(),
        State:          BlockStateActive,
    }

    m.blocks[height] = pending
    m.insertHeightLocked(height)
}
```

### Part Routing

When a part arrives, route it to the correct pending block:

```go
func (m *PendingBlocksManager) HandlePart(
    height int64,
    round int32,
    index uint32,
    part *types.Part,
    peer p2p.ID,
) error {
    m.mtx.Lock()
    defer m.mtx.Unlock()

    pending, exists := m.blocks[height]
    if !exists {
        // Part for unknown height - ignore or buffer briefly
        return ErrUnknownHeight
    }

    if pending.State != BlockStateActive {
        return ErrBlockNotActive
    }

    // Verify part against PartSetHeader (merkle proof)
    if err := pending.Parts.AddPart(part); err != nil {
        return fmt.Errorf("invalid part: %w", err)
    }

    pending.PartsReceived.SetIndex(int(index), true)
    pending.Requests.MarkReceived(index)

    // Forward to consensus
    select {
    case m.partsChan <- types.PartInfo{Part: part, Height: height, Round: round}:
    default:
        // Channel full - this shouldn't happen with proper sizing
    }

    // Check completion
    if pending.Parts.IsComplete() {
        pending.State = BlockStateComplete
        m.onBlockComplete(pending)
    }

    return nil
}
```

### Request Scheduling with Prioritization

```go
func (m *PendingBlocksManager) ScheduleRequests() map[p2p.ID]*proptypes.WantParts {
    m.mtx.RLock()
    defer m.mtx.RUnlock()

    requests := make(map[p2p.ID]*proptypes.WantParts)

    // Process in height order (priority)
    for _, height := range m.heights {
        pending := m.blocks[height]
        if pending.State != BlockStateActive {
            continue
        }

        missing := pending.Requests.GetMissingParts()
        if len(missing) == 0 {
            continue
        }

        // Allocate requests to peers
        // Higher priority (lower height) gets more of the peer bandwidth
        priorityWeight := m.getPriorityWeight(height)
        m.allocateRequests(pending, missing, priorityWeight, requests)
    }

    return requests
}

func (m *PendingBlocksManager) getPriorityWeight(height int64) float64 {
    // Oldest block gets highest weight
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

    // Exponential decay: priority 0 = 1.0, priority 1 = 0.5, priority 2 = 0.25
    return 1.0 / float64(1<<idx)
}
```

### Integration with Reactor

```go
// Reactor uses PendingBlocksManager as the single source of truth for all blocks.
// ProposalCache has been removed - PendingBlocksManager handles both live and catchup.
type Reactor struct {
    // ... existing fields ...

    // Unified block manager - handles live proposals and catchup
    pendingBlocks *PendingBlocksManager
}

// AddProposal forwards to PendingBlocksManager
func (r *Reactor) AddProposal(cb *proptypes.CompactBlock) bool {
    return r.pendingBlocks.AddProposal(cb)
}

// getAllState retrieves block state from PendingBlocksManager
func (r *Reactor) getAllState(height int64, round int32, catchup bool) (
    *proptypes.CompactBlock, *proptypes.CombinedPartSet, *bits.BitArray, bool,
) {
    return r.pendingBlocks.GetAllState(height, round, catchup)
}

// GetProposal retrieves a proposal for consensus
func (r *Reactor) GetProposal(height int64, round int32) (*types.Proposal, *types.PartSet, bool) {
    return r.pendingBlocks.GetProposal(height, round)
}
```

### Catchup Request Handling

```go
// onBlockAdded is the callback triggered by PendingBlocksManager when a block is added.
// This triggers immediate catchup for commitment and header-sync sourced blocks.
func (r *Reactor) onBlockAdded(height int64, source BlockSource) {
    // Only trigger immediate catchup for catchup-sourced blocks.
    // Compact blocks from live gossip don't need immediate catchup.
    if source == SourceCommitment || source == SourceHeaderSync {
        r.ticker.Reset(RetryTime)
        go r.retryWants()
    }
}

// retryWants is the unified catchup mechanism using PendingBlocksManager.
func (r *Reactor) retryWants() {
    missingParts := r.pendingBlocks.GetMissingParts()
    if len(missingParts) == 0 {
        return
    }

    peers := r.getPeers()
    for _, info := range missingParts {
        // Build BitArray for missing parts
        missing := bits.NewBitArray(int(info.TotalParts * 2))
        for _, idx := range info.MissingIndices {
            missing.SetIndex(idx, true)
        }

        // Calculate how many parts to request
        var missingPartsCount int32
        if info.Catchup {
            // Catchup blocks need ALL original parts (no parity data)
            missingPartsCount = int32(len(info.MissingIndices))
        } else {
            // Live blocks with parity only need enough for erasure decoding
            missingPartsCount = countRemainingParts(int(info.TotalParts),
                int(info.TotalParts)-len(info.MissingIndices))
        }

        // Send wants to peers
        for _, peer := range peers {
            if peer.consensusPeerState.GetHeight() < info.Height {
                continue
            }
            // ... send WantParts message
        }
    }
}

// AddCommitment handles consensus learning about a committed block.
// The onBlockAdded callback triggers catchup automatically.
func (r *Reactor) AddCommitment(height int64, round int32, psh *types.PartSetHeader) {
    r.pendingBlocks.AddFromCommitment(height, round, psh)
}
```

### Headersync Integration

```go
// In reactor startup
func (r *Reactor) OnStart() error {
    // ... existing startup ...

    // Subscribe to verified headers
    headerCh := r.headerSync.Subscribe()

    // Create pending blocks manager with header verification
    r.pendingBlocks = NewPendingBlocksManager(
        r.Logger,
        r.partChan,
        &headersyncVerifier{reactor: r.headerSync},
        PendingBlocksConfig{
            MaxConcurrent: 5,
            MemoryBudget:  100 * 1024 * 1024,
        },
    )

    // Background goroutine to handle verified headers
    go r.handleVerifiedHeaders(headerCh)

    return nil
}

func (r *Reactor) handleVerifiedHeaders(ch <-chan *headersync.VerifiedHeader) {
    for vh := range ch {
        // A new header is verified - we might already have its compact block
        r.pendingBlocks.OnHeaderVerified(vh)
    }
}

func (m *PendingBlocksManager) OnHeaderVerified(vh *headersync.VerifiedHeader) {
    m.mtx.Lock()
    defer m.mtx.Unlock()

    height := vh.Header.Height

    // Case 1: We already have this block active (verified or unverified)
    if pending, exists := m.blocks[height]; exists {
        if pending.CompactBlock != nil {
            // Verify the compact block we already have
            if !bytes.Equal(pending.CompactBlock.BlockID().Hash, vh.BlockID.Hash) {
                // Mismatch! The compact block we had was wrong
                m.evictBlock(height)
                // Log security event
                return
            }
            // Mark as verified (was already downloading)
            pending.BlockID = vh.BlockID
            pending.HeaderVerified = time.Now()
        }
        return
    }

    // Case 2: No compact block yet - create header-only entry
    if !m.hasCapacity(0) {
        return // Will pick up when capacity available
    }

    pending := &PendingBlock{
        Height:         vh.Header.Height,
        BlockID:        vh.BlockID,
        HeaderVerified: time.Now(),
        State:          BlockStateHeaderOnly,
    }

    m.blocks[height] = pending
    m.insertHeight(height)
}
```

## Benefits

### 1. Seamless Block Pipelining

No artificial distinction between "live" and "catchup" modes. The same code path handles:
- Current block (H) that we're actively trying to finalize
- Next block (H+1) that we received early from a fast proposer
- Catchup blocks (H+N) when we're behind

### 2. Verification Before Allocation

Memory is only allocated for blocks with verified headers. An attacker cannot DoS a node by sending fake compact blocks for arbitrary heights.

### 3. Priority-Based Scheduling

Older blocks (lower heights) always get priority. This ensures FIFO completion which:
- Allows consensus to make progress on the oldest block
- Enables garbage collection as blocks complete
- Prevents "convoy effect" where all peers work on the newest block

### 4. Graceful Degradation

If headersync hasn't verified a header yet:
- For current height: proceed with proposer signature verification (existing behavior)
- For future heights: buffer the compact block, activate when header arrives

### 5. Bounded Memory

Fixed maximum concurrent blocks and memory budget. Backpressure naturally flows to headersync if we can't keep up.

## Migration

### Phase 1: Add PendingBlocksManager ✅ COMPLETE

1. Created `consensus/propagation/pending.go` with unified types
2. `PendingBlocksManager` replaces `ProposalCache` entirely
3. All block state managed through single unified path

### Phase 2: Merge ProposalCache into PendingBlocksManager ✅ COMPLETE

1. Removed `ProposalCache` embedding from Reactor
2. Removed dead code: `commitment_state.go` and `commitment_state_test.go`
3. Reactor forwards all calls to `PendingBlocksManager`
4. Unified catchup mechanism via event-driven callback:
   - `BlockSource` enum tracks how each block was added
   - `BlockAddedCallback` triggers immediate catchup for header/commitment sources
   - `onBlockAdded` in reactor handles the callback
5. Three clean entry points:
   - `AddProposal(cb)` - live compact blocks from proposal gossip
   - `AddFromHeader(vh)` - verified headers from headersync
   - `AddFromCommitment(height, round, psh)` - PartSetHeader from consensus commit
6. Key behaviors:
   - `GetMissingParts` returns missing parts info with `Catchup` flag
   - Catchup blocks request ALL original parts; live blocks use erasure coding
   - Lower heights have priority - at capacity, higher-height proposals are rejected (FIFO completion)

### Phase 3: Integrate Headersync (Future)

1. Subscribe to headersync verified headers
2. Verify compact blocks against verified headers
3. Enable header-only pending blocks

### Phase 4: Remove Blocksync (Future)

1. Remove blocksync reactor
2. Remove SwitchToConsensus handoff
3. Update state sync to use new mechanism

## Key Implementation Details

### Catchup vs Live Block Distinction

The `CombinedPartSet` tracks whether a block is "catchup-only" via the `catchup` field:

```go
// IsCatchup returns true if this is a catchup-only block (no parity data available).
func (cps *CombinedPartSet) IsCatchup() bool {
    return cps.catchup
}
```

- **Live blocks** (from compact blocks): Have parity data, can use erasure coding to decode with only half the parts
- **Catchup blocks** (from commitments): No parity data, must download ALL original parts

### Capacity Management

**Lower heights have priority** - blocks must be completed in FIFO order. When at capacity, higher-height proposals are rejected (not evicted):

```go
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
```

This ensures:
- Oldest blocks complete first (FIFO)
- The proposer can always propose their own block (even if it exceeds memory budget when manager is empty)
- Backpressure naturally flows when at capacity

### Round 0 Special Case

Round 0 is treated as "unknown" for catchup blocks, meaning parts with any round will be accepted:

```go
if pending.Round != round && pending.Round != 0 {
    return false, nil // Wrong round
}
```

## Configuration

```toml
[propagation]
# Maximum concurrent block downloads
max_concurrent_blocks = 5

# Memory budget for pending blocks (bytes)
pending_blocks_memory = 104857600  # 100 MB

# Request timeout before retry
request_timeout = "15s"

# Maximum outstanding requests per peer
max_requests_per_peer = 32

# Whether to require header verification for non-current blocks
require_header_verification = true
```

## Consequences

### Positive

- **No mode switching**: Single unified path for all block downloads
- **Better pipelining**: Can work on H+1 while finishing H
- **Verified trust**: Only allocate memory for blocks we can verify
- **Bounded memory**: Predictable resource usage
- **Priority scheduling**: Ensures oldest blocks complete first

### Negative

- **Complexity**: More complex than single-block-at-a-time
- **Testing**: Requires careful testing of concurrent scenarios
- **Headersync dependency**: Optimal behavior requires headersync to be fast

### Neutral

- **Protocol compatibility**: No wire protocol changes required
- **Gradual rollout**: Can be deployed incrementally

## Alternatives Considered

### 1. Keep Separate Catchup Coordinator

The ADR-013 approach with a separate catchup coordinator creates:
- Mode switching complexity
- Duplicate request tracking logic
- Awkward integration points

A unified approach is cleaner.

### 2. Simple Queue of Blocks

A simple FIFO queue without priority weighting:
- Doesn't allow concurrent work on multiple blocks
- Slower catchup when behind
- Simpler but less performant

### 3. Full Pipeline with No Limits

Download all verified headers in parallel:
- Unbounded memory usage
- DoS vulnerability
- Network congestion

Bounded concurrency is necessary.

## References

- ADR-012: Header Sync Reactor
- Current propagation reactor: `consensus/propagation/reactor.go`
