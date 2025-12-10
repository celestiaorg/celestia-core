# ADR 014: Pipelined Block Propagation with Header-First Verification

## Changelog

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
// PendingBlock represents a block being downloaded.
// Created when either:
// 1. A compact block arrives AND headersync has verified its header
// 2. A verified header arrives from headersync (without compact block yet)
type PendingBlock struct {
    Height        int64
    Round         int32
    BlockID       types.BlockID  // From verified header

    // Block data - allocated lazily
    Parts         *types.PartSet
    PartsReceived *bits.BitArray

    // Compact block metadata (may be nil if header arrived first)
    CompactBlock  *proptypes.CompactBlock
    TxsAvailable  []bool  // Which txs we have in mempool

    // Request tracking
    Requests      *RequestTracker

    // Timing
    HeaderVerified time.Time
    StartedAt      time.Time

    // State
    State         PendingBlockState
}

type PendingBlockState int
const (
    // BlockStateHeaderOnly - we have a verified header but no compact block
    BlockStateHeaderOnly PendingBlockState = iota
    // BlockStateActive - we have both header and compact block, downloading parts
    BlockStateActive
    // BlockStateComplete - all parts received
    BlockStateComplete
)

// PendingBlocksManager manages concurrent block downloads.
type PendingBlocksManager struct {
    mtx           sync.RWMutex

    // Pending blocks indexed by height
    blocks        map[int64]*PendingBlock

    // Priority queue by height (lowest = highest priority)
    heights       []int64

    // Constraints
    maxConcurrent int    // e.g., 5 blocks
    memoryBudget  int64  // e.g., 100 MB
    currentMemory int64

    // Verified headers from headersync
    headerStore   HeaderVerifier

    // Output
    partsChan     chan<- types.PartInfo
}

// HeaderVerifier provides access to verified headers.
type HeaderVerifier interface {
    // GetVerifiedHeader returns a verified header if available.
    GetVerifiedHeader(height int64) (*types.Header, *types.BlockID, bool)

    // Subscribe returns a channel of newly verified headers.
    Subscribe() <-chan *headersync.VerifiedHeader
}
```

### Compact Block Processing Flow

```go
func (m *PendingBlocksManager) HandleCompactBlock(cb *proptypes.CompactBlock) error {
    m.mtx.Lock()
    defer m.mtx.Unlock()

    height := cb.Proposal.Height

    // Case 1: Already have this block in progress
    if pending, exists := m.blocks[height]; exists {
        if pending.CompactBlock != nil {
            return nil // Duplicate, ignore
        }
        // We had header-only, now we have compact block
        return m.activateBlock(pending, cb)
    }

    // Case 2: New block - verify header first
    header, blockID, verified := m.headerStore.GetVerifiedHeader(height)
    if !verified {
        // Header not yet verified - this is the "live" case
        // We can still process it, but we trust the proposer signature
        // This maintains backwards compatibility
        return m.addUnverifiedBlock(cb)
    }

    // Case 3: Header verified - validate compact block matches
    if !bytes.Equal(blockID.Hash, cb.BlockID().Hash) {
        return fmt.Errorf("compact block hash %X doesn't match verified header %X",
            cb.BlockID().Hash, blockID.Hash)
    }

    // Add as verified block
    return m.addVerifiedBlock(cb, header, blockID)
}

func (m *PendingBlocksManager) addVerifiedBlock(
    cb *proptypes.CompactBlock,
    header *types.Header,
    blockID *types.BlockID,
) error {
    // Check capacity
    if !m.hasCapacity(int64(cb.Proposal.PartSetHeader.Total)) {
        return ErrNoCapacity
    }

    pending := &PendingBlock{
        Height:         cb.Proposal.Height,
        Round:          cb.Proposal.Round,
        BlockID:        *blockID,
        CompactBlock:   cb,
        HeaderVerified: time.Now(),
        StartedAt:      time.Now(),
        State:          BlockStateActive,
    }

    // Allocate part set based on verified header
    pending.Parts = types.NewPartSetFromHeader(blockID.PartSetHeader)
    pending.PartsReceived = bits.NewBitArray(int(blockID.PartSetHeader.Total))
    pending.Requests = NewRequestTracker(int(blockID.PartSetHeader.Total))

    m.blocks[cb.Proposal.Height] = pending
    m.insertHeight(cb.Proposal.Height)
    m.currentMemory += m.estimateMemory(pending)

    return nil
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
// Reactor now uses PendingBlocksManager instead of ProposalCache for block downloads.
type Reactor struct {
    // ... existing fields ...

    // Replaces ProposalCache for multi-block management
    pendingBlocks *PendingBlocksManager

    // Still keep ProposalCache for serving block data to peers
    // (we may have complete blocks in store that others need)
    proposalCache *ProposalCache
}

func (r *Reactor) handleCompactBlock(cb *proptypes.CompactBlock, from p2p.ID) {
    // Route to pending blocks manager
    if err := r.pendingBlocks.HandleCompactBlock(cb); err != nil {
        if errors.Is(err, ErrNoCapacity) {
            r.Logger.Debug("No capacity for new block", "height", cb.Proposal.Height)
            return
        }
        r.Logger.Error("Failed to handle compact block", "err", err)
        return
    }

    // Also keep in proposal cache for serving to peers
    r.proposalCache.AddProposal(cb)

    // Broadcast to other peers
    r.broadcastCompactBlock(cb, from)
}

func (r *Reactor) handleRecoveryPart(peer p2p.ID, part *proptypes.RecoveryPart) {
    // ... validation ...

    typedPart := &types.Part{
        Index: part.Index,
        Bytes: part.Data,
        Proof: *part.Proof,
    }

    // Route to pending blocks manager
    if err := r.pendingBlocks.HandlePart(
        part.Height, part.Round, part.Index, typedPart, peer,
    ); err != nil {
        if errors.Is(err, ErrUnknownHeight) {
            // Maybe this is for a height we're not tracking
            // Could be out-of-order delivery
            r.Logger.Debug("Part for unknown height", "height", part.Height)
        }
        return
    }

    // Clear wants and propagate
    go r.clearWants(part, typedPart.Proof)
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

### Phase 1: Add PendingBlocksManager

1. Create `consensus/propagation/pending.go` with new types
2. Wire into reactor alongside existing ProposalCache
3. Route parts through PendingBlocksManager first, fall back to ProposalCache

### Phase 2: Integrate Headersync

1. Subscribe to headersync verified headers
2. Verify compact blocks against verified headers
3. Enable header-only pending blocks

### Phase 4: Remove Blocksync

1. Remove blocksync reactor
2. Remove SwitchToConsensus handoff
3. Update state sync to use new mechanism

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
