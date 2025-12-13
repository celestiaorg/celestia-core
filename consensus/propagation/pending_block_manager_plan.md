# Implementation Plan: Unified Pending Block Manager

## Overview

This plan describes how to implement a unified `PendingBlocksManager` that consolidates:
1. **Catchup** (part-level recovery for current consensus height)
2. **Blocksync** (downloading historical blocks to catch up to the network)

Both are unified under a single state machine that uses verified headers from headersync to download block parts and feed them to the consensus reactor **in order**.

## Current Architecture Problems

1. **Catchup is single-block**: The current `AddCommitment` can only track one height's parts at a time, causing slow sync under high load.
2. **Blocksync requests full blocks**: The `blocksync.Reactor` requests entire blocks from single peers, which is slow for large blocks.
3. **No coordination**: Blocksync and catchup don't share state, causing duplicate work.
4. **Ordering requirement**: The consensus reactor's `syncData` routine can only process one height at a time, but blocksync waits for two consecutive blocks (H and H+1) for commit verification.

## Design Principles

1. **Three valid entry points**: A block enters the manager via:
   - **CompactBlock (live proposal)**: Signed by the proposer - immediately valid for current height
   - **Verified header (headersync)**: For blocksync - header commit verified by +2/3 validators
   - **Commitment (consensus)**: When consensus has +2/3 precommits but lacks block data

2. **Part-level parallelism**: Download parts from multiple peers simultaneously.

3. **Ordered delivery for blocksync**: Historical blocks MUST be delivered to consensus in strictly increasing height order. Live consensus parts flow immediately.

4. **Memory-bounded**: Limit total pending blocks to avoid OOM. Never evict - simply don't request what we can't handle.

5. **Priority by height**: Lower heights have higher priority (must be processed first). Always start from the lowest height we don't have (like headersync).

6. **Proof requirements differ by source**:
   - CompactBlock: Has proof cache from proposal - parts don't need inline proofs
   - Header/Commitment: No proof cache - parts MUST include Merkle proofs (`Prove=true`)

---

## Compatibility with Existing Propagation Reactor

The `PendingBlocksManager` **replaces** `ProposalCache` but **preserves** all other propagation reactor functionality:

### What Is REPLACED

| Component | Current | New |
|-----------|---------|-----|
| `ProposalCache` | `map[height][round]*proposalData` | `PendingBlocksManager` |
| `AddCommitment` | Creates single catchup entry | Delegates to `AddFromCommitment` |
| `AddProposal` | Stores in ProposalCache | Delegates to `PendingBlocksManager.AddProposal` |
| `getAllState` | Reads from ProposalCache | Reads from `PendingBlocksManager` |
| `unfinishedHeights` | Iterates ProposalCache | `GetMissingParts` |
| `retryWants` | Single-height catchup | Multi-height part requests |

### What Is PRESERVED (no changes)

- **P2P channels**: `DataChannel (0x50)`, `WantChannel (0x51)` - unchanged
- **Message types**: `CompactBlock`, `HaveParts`, `WantParts`, `RecoveryPart` - unchanged
- **Peer state**: `PeerState` with haves/wants/requests tracking - unchanged
- **Have/Want protocol**: `handleHaves`, `handleWants`, `sendWant`, `broadcastHaves` - unchanged
- **Part handling**: `handleRecoveryPart`, `clearWants` - routing changes only
- **Compact block validation**: `validateCompactBlock` - unchanged
- **Mempool recovery**: `recoverPartsFromMempool` - unchanged
- **Proposal broadcasting**: `broadcastCompactBlock` - unchanged
- **Proposer flow**: `ProposeBlock` - unchanged

### Integration Points

```
┌─────────────────────────────────────────────────────────────────────┐
│                      Propagation Reactor                            │
│                                                                     │
│  ┌───────────────────┐     ┌─────────────────────────────────────┐ │
│  │ PendingBlocks     │     │ PRESERVED (unchanged)               │ │
│  │ Manager           │     │                                     │ │
│  │                   │     │ - handleHaves()                     │ │
│  │ AddProposal()────────────► handleCompactBlock() integration   │ │
│  │ AddFromHeader()   │     │ - handleWants()                     │ │
│  │ AddFromCommitment()     │ - handleRecoveryPart() ───────────────► routes parts
│  │ HandlePart()      │     │ - broadcastHaves()                  │ │
│  │ GetMissingParts() │     │ - clearWants()                      │ │
│  │ Prune()           │     │ - PeerState management              │ │
│  └───────────────────┘     └─────────────────────────────────────┘ │
└─────────────────────────────────────────────────────────────────────┘
```

---

## Block vs Parts: Delivery Mechanism Clarified

### TWO MODES of delivery to consensus

The consensus reactor receives data in **two different modes** depending on whether it's doing live consensus or blocksync:

#### Mode 1: LIVE CONSENSUS (Parts-Based)

When the node is participating in live consensus at height H:

```
Propagation Reactor                    Consensus Reactor
       │                                      │
       │  partChan <- PartInfo{Part, H, R}    │
       ├─────────────────────────────────────►│
       │                                      │ addProposalBlockPart()
       │                                      │ - Adds to rs.ProposalBlockParts
       │                                      │ - When complete, unmarshals Block
       │                                      │ - Triggers enterPrevote/tryFinalizeCommit
       │                                      │
       │  proposalChan <- ProposalAndSrc      │
       ├─────────────────────────────────────►│
       │                                      │ setProposal()
```

**Key points**:
- Parts flow through `partChan` one at a time
- Consensus accumulates parts in `rs.ProposalBlockParts`
- Block is reconstructed when `IsComplete()` returns true
- This is the EXISTING behavior - **no changes needed**

#### Mode 2: BLOCKSYNC (Full Blocks)

When the node is syncing historical blocks (height < consensus height):

```
Headersync          PendingBlocks           Consensus
    │               Manager                  Reactor
    │                   │                       │
    │ VerifiedHeader    │                       │
    ├──────────────────►│                       │
    │                   │ (downloads parts)     │
    │                   │                       │
    │                   │ CompletedBlock        │
    │                   ├──────────────────────►│ applyBlocksyncBlock()
    │                   │                       │ - Verify commit
    │                   │                       │ - SaveBlock to store
    │                   │                       │ - ApplyVerifiedBlock
    │                   │                       │ - Advance state
```

**Key points**:
- Full blocks (all parts collected) are passed via new `blockChan`
- Blocks MUST arrive in strictly increasing height order
- Consensus applies them directly (no part accumulation)
- This is NEW functionality

### Why Two Modes?

| Aspect | Live Consensus | Blocksync |
|--------|---------------|-----------|
| Height | Current consensus height | Historical heights |
| Parts arrive | Incrementally, out of order | Must be complete before delivery |
| Validation | Via proposal signature + votes | Via verified header commit |
| State machine | Full consensus rounds | Direct state application |
| Part source | CompactBlock with proofs | Header with proofs |

### Delivery Channel Design

```go
// Existing channels (unchanged)
partChan     chan types.PartInfo      // Live consensus parts
proposalChan chan ProposalAndSrc      // Live proposals

// NEW channel for blocksync
blockChan    chan *CompletedBlock     // Complete historical blocks

type CompletedBlock struct {
    Height    int64
    Round     int32
    Parts     *types.PartSet          // All original parts, complete
    BlockID   types.BlockID
    Commit    *types.Commit           // From headersync
}
```

### Routing Logic in `handleRecoveryPart`

The existing `handleRecoveryPart` routes parts based on context:

```go
func (blockProp *Reactor) handleRecoveryPart(peer p2p.ID, part *RecoveryPart) {
    // ... existing validation ...

    // Route to PendingBlocksManager
    added, complete, err := blockProp.pendingBlocks.HandlePart(part.Height, part.Round, part, proof)

    // If this is the CURRENT consensus height, also send to partChan (live mode)
    if part.Height == blockProp.currentConsensusHeight() && part.Index < parts.Original().Total() {
        blockProp.partChan <- types.PartInfo{...}  // EXISTING behavior
    }

    // If blocksync block is complete, it goes through blockChan
    // (handled by PendingBlocksManager's completion logic)
}
```

### Height Boundary: Live vs Blocksync

```
                    consensusHeight
                          │
    ◄─── Blocksync ───►   │   ◄─── Live Consensus ───►
                          │
    H-5  H-4  H-3  H-2  H-1   H   H+1  H+2
     │    │    │    │    │    │
     └────┴────┴────┴────┘    │
     Complete blocks via      │
     blockChan                │
                              │
                         Parts via
                         partChan
```

**Boundary rule**: Parts for height == consensusHeight flow through `partChan` (live mode). Parts for height < consensusHeight accumulate in PendingBlocksManager and complete blocks flow through `blockChan` (blocksync mode).

---

## Phase 1: PendingBlocksManager Core Data Structure

### Step 1.1: Create the `PendingBlock` struct

**File**: `consensus/propagation/pending_blocks.go`

```go
type BlockSource int

const (
    SourceCompactBlock BlockSource = iota  // Live proposal from gossip
    SourceHeaderSync                        // Verified header from headersync
    SourceCommitment                        // PartSetHeader from consensus commit
)

type PendingBlockState int

const (
    BlockStateActive PendingBlockState = iota  // Downloading parts
    BlockStateComplete                          // All original parts received
)

type PendingBlock struct {
    Height         int64
    Round          int32
    Source         BlockSource           // How this block was FIRST added
    BlockID        types.BlockID         // From verified header (may be partial if from commitment)
    Parts          *CombinedPartSet      // Original + parity parts
    CompactBlock   *CompactBlock         // Nil until/unless CompactBlock arrives
    MaxRequests    *bits.BitArray        // Per-part request tracking
    HeaderVerified bool                  // Whether headersync verified header
    HasCommitment  bool                  // Whether consensus provided commitment
    Commit         *types.Commit         // From headersync (for blocksync application)
    CreatedAt      time.Time
    State          PendingBlockState
    allocatedBytes int64                 // Memory tracking
}

// CanUseProofCache returns true if we have a CompactBlock with proof cache
func (pb *PendingBlock) CanUseProofCache() bool {
    return pb.CompactBlock != nil && len(pb.CompactBlock.PartsHashes) > 0
}

// NeedsInlineProofs returns true if parts must include Merkle proofs
func (pb *PendingBlock) NeedsInlineProofs() bool {
    return !pb.CanUseProofCache()
}
```

**Testing Criteria**:
- [ ] Unit test: `TestPendingBlockCreation` - verify struct initialization
- [ ] Unit test: `TestPendingBlockStateTransitions` - verify Active→Complete transition

### Step 1.2: Create the `PendingBlocksManager` struct

**File**: `consensus/propagation/pending_blocks.go`

```go
type PendingBlocksConfig struct {
    MaxConcurrent int   // Max blocks tracked (default: 500)
    MemoryBudget  int64 // Max memory in bytes (default: 12GiB)
}

type PendingBlocksManager struct {
    mtx           sync.RWMutex
    logger        log.Logger
    blockStore    *store.BlockStore

    // Sorted by height (ascending) for priority
    heights       []int64
    blocks        map[int64]*PendingBlock  // height -> block

    // Memory management
    config        PendingBlocksConfig
    currentMemory int64

    // Output channel for completed blocks
    completedBlocks chan *CompletedBlock
}

type CompletedBlock struct {
    Height  int64
    Round   int32
    Parts   *types.PartSet
    BlockID types.BlockID
}
```

**Testing Criteria**:
- [ ] Unit test: `TestManagerCreation` - verify default config
- [ ] Unit test: `TestManagerHeightOrdering` - verify heights slice stays sorted

---

## Phase 2: Entry Point Methods

### Step 2.1: `AddProposal` - Live compact blocks

Called when a CompactBlock is received from gossip. Can either create a new PendingBlock or attach to an existing one (if header/commitment arrived first).

```go
func (m *PendingBlocksManager) AddProposal(cb *CompactBlock) (added bool, err error) {
    m.mtx.Lock()
    defer m.mtx.Unlock()

    existing := m.blocks[cb.Proposal.Height]
    if existing != nil {
        // ATTACH to existing block (header or commitment arrived first)
        if existing.CompactBlock != nil {
            return false, nil  // Already have CompactBlock, ignore duplicate
        }

        // Validate BlockID matches if we have a verified header
        if existing.HeaderVerified {
            if !bytes.Equal(existing.BlockID.Hash, cb.Proposal.BlockID.Hash) {
                return false, fmt.Errorf("CompactBlock hash mismatch with verified header")
            }
        }

        // Validate PartSetHeader matches if we have a commitment
        if existing.HasCommitment {
            if existing.BlockID.PartSetHeader != cb.Proposal.BlockID.PartSetHeader {
                return false, fmt.Errorf("CompactBlock PSH mismatch with commitment")
            }
        }

        // Attach CompactBlock - now we have proof cache!
        existing.CompactBlock = cb
        return true, nil
    }

    // NEW block - check capacity/memory limits
    if !m.canAddBlock(m.estimateBlockMemory(&cb.Proposal.BlockID.PartSetHeader)) {
        return false, fmt.Errorf("capacity exceeded")
    }

    // Create new PendingBlock
    pb := &PendingBlock{
        Height:       cb.Proposal.Height,
        Round:        cb.Proposal.Round,
        Source:       SourceCompactBlock,
        BlockID:      cb.Proposal.BlockID,
        CompactBlock: cb,
        Parts:        NewCombinedSetFromCompactBlock(cb),
        // ... initialization ...
    }
    m.addBlock(pb)
    return true, nil
}
```

**Testing Criteria**:
- [ ] Unit test: `TestAddProposal_NewBlock` - adds new block correctly
- [ ] Unit test: `TestAddProposal_Duplicate` - rejects duplicate CompactBlock
- [ ] Unit test: `TestAddProposal_AttachToHeader` - attaches to existing header-created block
- [ ] Unit test: `TestAddProposal_AttachToCommitment` - attaches to existing commitment-created block
- [ ] Unit test: `TestAddProposal_HashMismatch` - rejects if BlockID doesn't match header
- [ ] Unit test: `TestAddProposal_PSHMismatch` - rejects if PSH doesn't match commitment
- [ ] Unit test: `TestAddProposal_CapacityLimit` - respects MaxConcurrent
- [ ] Unit test: `TestAddProposal_MemoryLimit` - respects MemoryBudget (with a smaller configuration)

### Step 2.2: `AddFromHeader` - Verified headers from headersync

Called when headersync verifies a new header. Can create new block or upgrade existing commitment-created block.

**Important**: This method should always succeed for valid headers. Capacity is enforced at the **request layer** - we simply don't fetch headers when at capacity. By the time `AddFromHeader` is called, we've already committed to tracking this block.

```go
func (m *PendingBlocksManager) AddFromHeader(vh *VerifiedHeader) (added bool, err error) {
    m.mtx.Lock()
    defer m.mtx.Unlock()

    existing := m.blocks[vh.Header.Height]
    if existing != nil {
        if existing.HeaderVerified {
            return false, nil  // Already have verified header
        }

        // UPGRADE existing block (was created from commitment)
        // Validate PSH matches
        if existing.HasCommitment {
            if existing.BlockID.PartSetHeader != vh.BlockID.PartSetHeader {
                return false, fmt.Errorf("header PSH mismatch with commitment")
            }
        }

        // Validate against CompactBlock if present
        if existing.CompactBlock != nil {
            if !bytes.Equal(existing.CompactBlock.Proposal.BlockID.Hash, vh.BlockID.Hash) {
                return false, fmt.Errorf("header hash mismatch with CompactBlock")
            }
        }

        existing.BlockID = vh.BlockID
        existing.HeaderVerified = true
        existing.Commit = vh.Commit
        return true, nil
    }

    // NEW block - capacity was checked before we fetched this header
    // (see tryFillCapacity). Just add it.
    pb := &PendingBlock{
        Height:         vh.Header.Height,
        Round:          0,  // Round comes from commit or CompactBlock later
        Source:         SourceHeaderSync,
        BlockID:        vh.BlockID,
        HeaderVerified: true,
        Commit:         vh.Commit,
        Parts:          NewCombinedPartSetFromHeader(vh.BlockID.PartSetHeader),
        // ... initialization ...
    }
    m.addBlock(pb)
    return true, nil
}
```

**Testing Criteria**:
- [ ] Unit test: `TestAddFromHeader_NewBlock` - adds new header-only block
- [ ] Unit test: `TestAddFromHeader_Duplicate` - ignores if already have verified header
- [ ] Unit test: `TestAddFromHeader_UpgradeCommitment` - upgrades commitment-created block
- [ ] Unit test: `TestAddFromHeader_ValidateCompactBlock` - validates against existing CompactBlock
- [ ] Unit test: `TestAddFromHeader_PSHMismatch` - rejects if PSH doesn't match commitment

### Step 2.3: `AddFromCommitment` - Consensus commits

Called when consensus has +2/3 precommits but lacks block data. Creates minimal block that can be upgraded later.

```go
func (m *PendingBlocksManager) AddFromCommitment(height int64, round int32, psh *types.PartSetHeader) (added bool) {
    m.mtx.Lock()
    defer m.mtx.Unlock()

    existing := m.blocks[height]
    if existing != nil {
        // Already tracking this height - just mark that we have commitment
        if !existing.HasCommitment {
            existing.HasCommitment = true
            // Validate PSH matches if we have data
            if existing.BlockID.PartSetHeader.Total > 0 {
                if existing.BlockID.PartSetHeader != *psh {
                    m.logger.Error("commitment PSH mismatch", "height", height)
                    // Don't return error - commitment is authoritative
                }
            }
            existing.BlockID.PartSetHeader = *psh
        }
        return false  // Already existed
    }

    // NEW block from commitment
    // BYPASS capacity limits - commitments MUST be accepted
    pb := &PendingBlock{
        Height:        height,
        Round:         round,
        Source:        SourceCommitment,
        BlockID:       types.BlockID{PartSetHeader: *psh},  // Only PSH known
        HasCommitment: true,
        Parts:         NewCombinedPartSetFromPSH(*psh, true),  // catchup=true
        // ... initialization ...
    }
    m.addBlock(pb)  // Bypasses capacity check
    return true
}
```

**Testing Criteria**:
- [ ] Unit test: `TestAddFromCommitment_NewBlock` - adds commitment block
- [ ] Unit test: `TestAddFromCommitment_ExistingBlock` - marks existing block as having commitment
- [ ] Unit test: `TestAddFromCommitment_BypassLimits` - always accepts, ignores capacity
- [ ] Unit test: `TestAddFromCommitment_PSHMismatch` - logs but accepts (commitment is authoritative)

---

## Phase 3: Part Handling

### Step 3.1: `HandlePart` - Receive parts

```go
func (m *PendingBlocksManager) HandlePart(height int64, round int32, part *RecoveryPart, proof merkle.Proof) (bool, error) {
    // 1. Look up pending block
    // 2. Verify part with proof (if no CompactBlock) or with cached proof
    // 3. Add to CombinedPartSet
    // 4. Check if original parts complete -> transition to BlockStateComplete
    // 5. If complete, send to completedBlocks channel
    // Return: (added, error)
}
```

**Testing Criteria**:
- [ ] Unit test: `TestHandlePart_Valid` - adds valid part
- [ ] Unit test: `TestHandlePart_InvalidProof` - rejects invalid proof
- [ ] Unit test: `TestHandlePart_UnknownHeight` - handles gracefully
- [ ] Unit test: `TestHandlePart_Completion` - transitions to Complete
- [ ] Unit test: `TestHandlePart_ProofCacheUpgrade` - parts rejected for missing proofs stay rejected even after a CompactBlock later provides a cache
- [ ] Unit test: `TestHandlePart_DuplicateOrStale` - duplicate parts and parts for heights already pruned are ignored without side effects

### Step 3.2: `GetMissingParts` - For request generation

```go
func (m *PendingBlocksManager) GetMissingParts(maxBlocks int) []*MissingPartsInfo {
    // Returns info about missing parts for up to maxBlocks pending blocks
    // Ordered by height (lowest first)
    type MissingPartsInfo struct {
        Height  int64
        Round   int32
        Missing *bits.BitArray
        Total   uint32
    }
}
```

**Testing Criteria**:
- [ ] Unit test: `TestGetMissingParts_Ordering` - returns lowest heights first
- [ ] Unit test: `TestGetMissingParts_OnlyActive` - excludes Complete blocks
- [ ] Unit test: `TestGetMissingParts_Limit` - respects maxBlocks

---

## Phase 4: Memory Management & Pruning

### Step 4.1: Memory accounting

The manager tracks memory usage but **never evicts** and **never rejects** blocks. Capacity is enforced at the **request layer** - we simply don't request headers for new blocks when at capacity. Once we've decided to request a block, we're committed to tracking it.

**Key principle**: Don't request what you can't handle. Check capacity BEFORE requesting, not when adding.

```go
func (m *PendingBlocksManager) estimateBlockMemory(psh *types.PartSetHeader) int64 {
    // partSize * totalParts * 2 (for parity)
    return int64(types.BlockPartSizeBytes) * int64(psh.Total) * 2
}

// HasCapacity returns true if we can add at least one more block.
// Called by tryFillCapacity BEFORE fetching headers.
func (m *PendingBlocksManager) HasCapacity() bool {
    m.mtx.RLock()
    defer m.mtx.RUnlock()
    // Conservative estimate - assume average block size
    avgBlockMemory := int64(types.BlockPartSizeBytes) * 100 * 2  // ~100 parts average
    return m.currentMemory + avgBlockMemory <= m.config.MemoryBudget &&
           len(m.blocks) < m.config.MaxConcurrent
}

// AvailableCapacity returns how many more blocks we can track.
// Used to determine how many headers to fetch.
func (m *PendingBlocksManager) AvailableCapacity() int {
    m.mtx.RLock()
    defer m.mtx.RUnlock()
    return m.config.MaxConcurrent - len(m.blocks)
}
```

**Testing Criteria**:
- [ ] Unit test: `TestMemoryEstimation` - correct calculation
- [ ] Unit test: `TestHasCapacity_UnderLimit` - returns true when under limit
- [ ] Unit test: `TestHasCapacity_AtLimit` - returns false when at limit
- [ ] Unit test: `TestAvailableCapacity` - returns correct count

### Step 4.2: `Prune` - Clean up after commit

```go
func (m *PendingBlocksManager) Prune(committedHeight int64) {
    // Remove all blocks with height <= committedHeight
    // Update currentMemory
}
```

**Testing Criteria**:
- [ ] Unit test: `TestPrune_RemovesOld` - removes heights <= committed
- [ ] Unit test: `TestPrune_KeepsNew` - keeps heights > committed
- [ ] Unit test: `TestPrune_MemoryUpdate` - updates currentMemory correctly

---

## Phase 5: Integration with Headersync

### Step 5.1: On-demand header fetching

Headers are fetched **immediately when needed**, not via polling or push subscriptions.

**Why on-demand?**
- No polling latency (100ms polling = 100ms wasted per header)
- No dropped headers from full subscription channels
- Headers are persisted to blockStore by headersync - always available to pull
- Fetch exactly when we need them, not before or after

**Available headersync APIs:**
```go
// Already exists - pulls from blockStore
GetVerifiedHeader(height int64) (*types.Header, *types.BlockID, bool)
GetCommit(height int64) *types.Commit

// Already exists - for height tracking
Height() int64           // Current headersync height (highest verified)
MaxPeerHeight() int64    // Highest peer height seen
IsCaughtUp() bool        // Whether headersync is caught up
```

**On-demand fetch points:**

Headers are fetched at these specific moments, **always starting from the lowest height we need** (like headersync does):

1. **When a block completes and we have capacity:**
```go
func (m *PendingBlocksManager) onBlockComplete(height int64) {
    // Space freed up - try to fill with next headers starting from lowest needed
    m.tryFillCapacity()
}
```

2. **When AddFromCommitment is called (consensus needs a block):**
```go
func (m *PendingBlocksManager) AddFromCommitment(height int64, round int32, psh *types.PartSetHeader) {
    // ... create pending block ...

    // Immediately check if headersync already has this header
    m.tryFetchHeader(height)
}
```

3. **When we start requesting parts (need to know if header is verified):**
```go
func (m *PendingBlocksManager) GetMissingParts(maxBlocks int) []*MissingPartsInfo {
    // For each pending block without verified header, try to fetch it now
    for _, block := range m.blocks {
        if !block.HeaderVerified {
            m.tryFetchHeader(block.Height)
        }
    }
    // ... return missing parts info ...
}
```

**Core fetch functions:**

```go
// tryFillCapacity fetches headers for consecutive heights starting from
// the lowest height we need, up to available capacity.
// This is the main entry point - always fills from lowest height first.
//
// KEY: Capacity is checked HERE, before fetching. We never request
// headers we can't handle.
func (m *PendingBlocksManager) tryFillCapacity() {
    startHeight := m.lowestNeededHeight()
    hsHeight := m.hsReader.Height()  // Highest verified header available

    for height := startHeight; height <= hsHeight; height++ {
        // CHECK CAPACITY BEFORE FETCHING - this is where we enforce limits
        if !m.HasCapacity() {
            return  // At capacity, don't request more headers
        }

        // Skip if we already have this height
        if m.hasHeight(height) {
            continue
        }

        // We have capacity - fetch and add this header
        m.fetchAndAddHeader(height)
    }
}

// lowestNeededHeight returns the lowest height we should be fetching.
// This is typically lastCommittedHeight + 1.
func (m *PendingBlocksManager) lowestNeededHeight() int64 {
    m.mtx.RLock()
    defer m.mtx.RUnlock()
    if len(m.heights) == 0 {
        return m.lastCommittedHeight + 1
    }
    // If we have pending blocks, start from lowest pending
    // (in case there are gaps below it)
    return min(m.heights[0], m.lastCommittedHeight + 1)
}

// fetchAndAddHeader fetches a header from headersync and adds it.
// Only called when we've already confirmed we have capacity.
func (m *PendingBlocksManager) fetchAndAddHeader(height int64) bool {
    header, blockID, ok := m.hsReader.GetVerifiedHeader(height)
    if !ok {
        return false  // Header not yet verified by headersync
    }

    commit := m.hsReader.GetCommit(height)
    if commit == nil {
        return false  // Commit not available yet
    }

    vh := &VerifiedHeader{
        Header:  header,
        BlockID: *blockID,
        Commit:  commit,
    }

    // AddFromHeader always succeeds (capacity was checked before we got here)
    added, _ := m.AddFromHeader(vh)
    return added
}

// Interface for headersync (for testing)
type HeaderSyncReader interface {
    GetVerifiedHeader(height int64) (*types.Header, *types.BlockID, bool)
    GetCommit(height int64) *types.Commit
    Height() int64
    MaxPeerHeight() int64
    IsCaughtUp() bool
}
```

**Testing Criteria**:
- [ ] Unit test: `TestFetchAndAddHeader_Available` - fetches when header exists
- [ ] Unit test: `TestFetchAndAddHeader_NotYetAvailable` - returns false, no error
- [ ] Unit test: `TestFetchAndAddHeader_UpgradesCommitmentBlock` - upgrades existing block
- [ ] Unit test: `TestFetchAndAddHeader_Idempotent` - repeated fetch/add for same height does not duplicate state
- [ ] Unit test: `TestFetchAndAddHeader_HeaderWithoutCommit` - header present but commit missing returns false and leaves state unchanged
- [ ] Unit test: `TestTryFillCapacity_StartsFromLowest` - always fetches lowest heights first
- [ ] Unit test: `TestTryFillCapacity_StopsAtCapacity` - stops requesting when at capacity (key test!)
- [ ] Unit test: `TestTryFillCapacity_SkipsExisting` - skips heights already tracked
- [ ] Unit test: `TestOnBlockComplete_TriggersRefill` - completing block triggers capacity refill

### Step 5.2: Subscription as wake-up signal for batch processing

The headersync subscription is used ONLY as a wake-up signal to process any headers that became available while we were busy. The actual data is always pulled via `tryFillCapacity`.

```go
func (r *Reactor) setupHeaderSyncWakeup(hsReactor *headersync.Reactor) {
    // Buffer of 1 - we only care about "something changed" signal
    wakeupCh := make(chan *headersync.VerifiedHeader, 1)
    hsReactor.Subscribe(wakeupCh)

    go func() {
        for {
            select {
            case <-r.ctx.Done():
                return
            case _, ok := <-wakeupCh:
                if !ok {
                    return
                }
                // New headers available - try to fill capacity starting from lowest needed
                // tryFillCapacity handles capacity checks internally
                r.pendingBlocks.tryFillCapacity()
            }
        }
    }()
}
```

**Testing Criteria**:
- [ ] Unit test: `TestWakeupSignal_TriggersFillCapacity` - notification triggers tryFillCapacity
- [ ] Unit test: `TestWakeupSignal_DroppedOK` - dropped notifications recovered by next signal

### Step 5.3: Modify existing catchup to use manager

Replace `AddCommitment` calls with manager:

```go
func (r *Reactor) AddCommitment(height int64, round int32, psh *types.PartSetHeader) {
    r.pendingBlocks.AddFromCommitment(height, round, psh)
    r.triggerPartRequests()
}
```

**Testing Criteria**:
- [ ] Unit test: `TestAddCommitment_UsesManager` - uses new manager
- [ ] Integration test: `TestCommitmentTriggersRequests` - triggers part requests

---

## Phase 6: Ordered Block Delivery to Consensus

### Step 6.1: Block delivery routine (for blocksync mode only)

This is the CRITICAL ordering component for blocksync. Complete blocks must be delivered in strictly increasing height order.

**Important**: This only handles blocksync (historical) blocks. Live consensus parts continue to use the existing `partChan` mechanism unchanged.

```go
type BlockDeliveryManager struct {
    mtx             sync.Mutex
    pendingBlocks   *PendingBlocksManager
    nextHeight      int64  // Next blocksync height to deliver
    readyBlocks     map[int64]*CompletedBlock
    blockChan       chan *CompletedBlock  // Output to consensus
}

func (d *BlockDeliveryManager) Run() {
    for completed := range d.pendingBlocks.completedBlocks {
        d.mtx.Lock()
        d.readyBlocks[completed.Height] = completed

        // Deliver all consecutive ready blocks IN ORDER
        for {
            block, ok := d.readyBlocks[d.nextHeight]
            if !ok {
                break
            }
            delete(d.readyBlocks, d.nextHeight)
            d.blockChan <- block  // Blocks here until consensus processes it
            d.nextHeight++
        }
        d.mtx.Unlock()
    }
}
```

**Testing Criteria**:
- [x] Unit test: `TestBlockDeliveryManager_Creation` - verify creation (IMPLEMENTED)
- [x] Unit test: `TestBlockDeliveryManager_BlockChan` - verify channel access (IMPLEMENTED)
- [ ] Unit test: `TestDelivery_InOrder` - delivers in height order
- [ ] Unit test: `TestDelivery_WaitsForGaps` - waits for missing heights
- [ ] Unit test: `TestDelivery_BatchDelivery` - delivers multiple consecutive blocks
- [ ] Integration test: `TestDelivery_ConsensusReceives` - consensus receives correctly
- [ ] Unit test: `TestDelivery_Backpressure` - blocks are buffered correctly when `blockChan` is slow/full
- [ ] Unit test: `TestDelivery_DuplicateOrStaleCompletion` - completions for heights < nextHeight are ignored without panicking

### Step 6.2: Modify `handleRecoveryPart` for dual routing (IMPLEMENTED)

The existing `handleRecoveryPart` must route parts to BOTH live consensus AND the pending blocks manager:

```go
func (blockProp *Reactor) handleRecoveryPart(peer p2p.ID, part *RecoveryPart) {
    // ... existing validation (unchanged) ...

    // Get proof from compact block or message
    proof := cb.GetProof(part.Index)
    if proof == nil {
        if part.Proof == nil {
            return
        }
        proof = part.Proof
    }

    // CHANGED: Route to PendingBlocksManager first
    added, err := blockProp.pendingBlocks.HandlePart(part.Height, part.Round, part, *proof)
    if err != nil {
        blockProp.Logger.Error("failed to add part", "err", err)
        return
    }
    if !added {
        return
    }

    // UNCHANGED: Still notify peer state
    if p := blockProp.getPeer(peer); p != nil {
        select {
        case p.receivedParts <- partData{height: part.Height, round: part.Round}:
        default:
        }
    }

    // CHANGED: Only send to partChan if this is LIVE CONSENSUS height
    // (blocksync blocks go through blockChan when complete)
    blockProp.pmtx.Lock()
    isLiveConsensus := part.Height == blockProp.height
    blockProp.pmtx.Unlock()

    if isLiveConsensus && part.Index < parts.Original().Total() {
        select {
        case <-blockProp.ctx.Done():
            return
        case blockProp.partChan <- types.PartInfo{
            Part:   &types.Part{Index: part.Index, Bytes: part.Data, Proof: *proof},
            Height: part.Height,
            Round:  part.Round,
        }:
        }
    }

    // UNCHANGED: Clear wants and handle decoding
    go blockProp.clearWants(part, *proof)
    // ... rest of decoding logic ...
}
```

**Testing Criteria**:
- [x] Unit test: `TestHandleRecoveryPart_LiveConsensus` - routes to partChan (IMPLEMENTED)
- [x] Unit test: `TestHandleRecoveryPart_HeightRouting` - verifies height-based routing (IMPLEMENTED)
- [ ] Unit test: `TestHandleRecoveryPart_Blocksync` - does NOT route to partChan (requires PendingBlocksManager integration)
- [ ] Unit test: `TestHandleRecoveryPart_BothPaths` - PendingBlocksManager always gets part (requires PendingBlocksManager integration)

### Step 6.3: Modify consensus `syncData` for blocksync (IMPLEMENTED)

The existing `syncData` adds a new case for complete blocksync blocks:

```go
func (cs *State) syncData() {
    partChan := cs.propagator.GetPartChan()
    proposalChan := cs.propagator.GetProposalChan()
    blockChan := cs.propagator.GetBlockChan()  // NEW: for complete blocksync blocks

    for {
        select {
        case <-cs.Quit():
            return

        // UNCHANGED: Live proposal handling
        case proposalAndFrom, ok := <-proposalChan:
            if !ok {
                return
            }
            cs.peerMsgQueue <- msgInfo{&ProposalMessage{&proposalAndFrom.Proposal}, proposalAndFrom.From}

        // UNCHANGED: Height/round change handling
        case _, ok := <-cs.newHeightOrRoundChan:
            // ... existing logic ...

        // UNCHANGED: Live consensus parts
        case part, ok := <-partChan:
            if !ok {
                return
            }
            // ... existing logic - routes to addProposalBlockPart ...

        // NEW: Complete blocksync blocks
        case block, ok := <-blockChan:
            if !ok {
                return
            }
            if err := cs.applyBlocksyncBlock(block); err != nil {
                cs.Logger.Error("failed to apply blocksync block", "height", block.Height, "err", err)
            }
        }
    }
}
```

### Step 6.4: Implement `applyBlocksyncBlock` (IMPLEMENTED)

This is the core blocksync application logic, similar to what `blocksync.Reactor.poolRoutine` does:

```go
func (cs *State) applyBlocksyncBlock(completed *CompletedBlock) error {
    // 1. Validate this is the next expected height
    cs.stateMtx.RLock()
    expectedHeight := cs.state.LastBlockHeight + 1
    cs.stateMtx.RUnlock()

    if completed.Height != expectedHeight {
        return fmt.Errorf("unexpected blocksync height: got %d, want %d",
            completed.Height, expectedHeight)
    }

    // 2. Reconstruct block from parts
    bz := completed.Parts.GetBytes()
    pbb := new(cmtproto.Block)
    if err := proto.Unmarshal(bz, pbb); err != nil {
        return fmt.Errorf("failed to unmarshal block: %w", err)
    }
    block, err := types.BlockFromProto(pbb)
    if err != nil {
        return fmt.Errorf("failed to convert block from proto: %w", err)
    }

    // 3. Verify commit (commit is from headersync's verified header)
    cs.stateMtx.RLock()
    state := cs.state
    cs.stateMtx.RUnlock()

    if err := state.Validators.VerifyCommitLight(
        cs.config.ChainID(), completed.BlockID, completed.Height, completed.Commit,
    ); err != nil {
        return fmt.Errorf("commit verification failed: %w", err)
    }

    // 4. Validate block structure
    if err := cs.blockExec.ValidateBlock(state, block); err != nil {
        return fmt.Errorf("block validation failed: %w", err)
    }

    // 5. Save block to store
    cs.blockStore.SaveBlock(block, completed.Parts, completed.Commit)

    // 6. Apply to state
    newState, err := cs.blockExec.ApplyVerifiedBlock(state, completed.BlockID, block, completed.Commit)
    if err != nil {
        return fmt.Errorf("failed to apply block: %w", err)
    }

    // 7. Update consensus state
    cs.stateMtx.Lock()
    cs.state = newState
    cs.stateMtx.Unlock()

    // 8. Prune propagator state
    cs.propagator.Prune(completed.Height)

    cs.Logger.Info("Applied blocksync block", "height", completed.Height)
    return nil
}
```

**Testing Criteria**:
- [ ] Unit test: `TestApplyBlocksyncBlock_Valid` - applies valid block
- [ ] Unit test: `TestApplyBlocksyncBlock_WrongHeight` - rejects wrong height
- [ ] Unit test: `TestApplyBlocksyncBlock_InvalidCommit` - rejects invalid commit
- [ ] Unit test: `TestApplyBlocksyncBlock_InvalidBlock` - rejects malformed block
- [ ] Integration test: `TestBlocksyncToConsensus` - full flow works

### Step 6.5: Add `GetBlockChan` to Propagator interface (PARTIALLY IMPLEMENTED)

The interface and stub implementation exist. The reactor currently returns `nil` - needs integration with `BlockDeliveryManager` when reactor is wired up.

```go
// In propagator.go
type Propagator interface {
    // ... existing methods ...
    GetBlockChan() <-chan *CompletedBlock  // NEW
}

// In reactor.go - currently returns nil, needs BlockDeliveryManager integration
func (r *Reactor) GetBlockChan() <-chan *CompletedBlock {
    return nil  // TODO: return r.blockDelivery.blockChan when integrated
}
```

**Testing Criteria**:
- [x] Interface added to `Propagator` (IMPLEMENTED)
- [x] `NoOpPropagator.GetBlockChan()` returns nil (IMPLEMENTED)
- [ ] Unit test: `TestGetBlockChan` - returns valid channel (requires BlockDeliveryManager integration)
- [ ] Unit test: `TestNoOpPropagator_GetBlockChan` - returns nil

---

## Phase 7: Request Coordination (IMPLEMENTED)

### Step 7.1: Unified part request routine (IMPLEMENTED)

The `requestMissingParts` function uses `PendingBlocksManager.GetMissingParts()` to request missing parts for all tracked pending blocks. When `pendingBlocks` is nil, it falls back to the legacy `retryWants()` for backwards compatibility.

Key features:
- Requests parts ordered by height (lowest first) for priority
- Filters peers by height (only requests from peers at or above the block's height)
- Uses `MissingPartsInfo.NeedsProofs` to set `Prove` flag correctly in WantParts messages
- Respects per-part request limits via `ReqLimit()` to avoid over-requesting

```go
func (r *Reactor) requestMissingParts() {
    missing := r.pendingBlocks.GetMissingParts(100)  // Top 100 blocks by priority
    peers := r.getPeers()

    for _, info := range missing {
        for _, peer := range peers {
            if peer.consensusPeerState.GetHeight() < info.Height {
                continue
            }

            // Calculate parts to request from this peer
            // Respect per-peer and per-part limits
            // Send WantParts message
        }
    }
}
```

**Testing Criteria**:
- [x] Unit test: `TestRequestMissingParts_PeerFiltering` - filters by peer height (IMPLEMENTED)
- [x] Unit test: `TestRequestMissingParts_RequestLimits` - respects limits (IMPLEMENTED)
- [x] Unit test: `TestRequestMissingParts_UsesNeedsProofs` - uses NeedsProofs flag correctly (IMPLEMENTED)
- [x] Unit test: `TestRequestMissingParts_FallbackToLegacy` - falls back to retryWants when pendingBlocks is nil (IMPLEMENTED)
- [x] Unit test: `TestRequestMissingParts_PriorityByHeight` - requests in height order (IMPLEMENTED)
- [x] Unit test: `TestRequestMissingParts_EmptyPeers` - handles no peers gracefully (IMPLEMENTED)
- [x] Unit test: `TestRequestMissingParts_NoMissingParts` - handles no missing parts gracefully (IMPLEMENTED)
- [ ] Integration test: `TestRequestResponse_Flow` - full request/response

### Step 7.2: Unified retry ticker (IMPLEMENTED)

The existing retry ticker in the reactor's constructor now calls `requestMissingParts()` instead of `retryWants()`:

```go
// start the catchup/blocksync retry routine
go func() {
    for {
        select {
        case <-reactor.ctx.Done():
            return
        case <-reactor.ticker.C:
            // Run the unified request routine to recover missing parts.
            // If pendingBlocks is configured, uses the new unified manager.
            // Otherwise falls back to legacy retryWants.
            reactor.requestMissingParts()
        }
    }
}()
```

**Testing Criteria**:
- [x] Ticker calls `requestMissingParts()` (verified via code inspection)
- [ ] Integration test: `TestRetryTicker_RecoversFromTimeout` - recovers stalled downloads

### Additional Implementation Details

**Reactor Fields Added**:
- `pendingBlocks *PendingBlocksManager` - manages pending blocks for catchup/blocksync
- `blockDelivery *BlockDeliveryManager` - ensures ordered block delivery for blocksync

**Reactor Options Added**:
- `WithPendingBlocksManager(mgr)` - configures the PendingBlocksManager
- `WithBlockDeliveryManager(mgr)` - configures the BlockDeliveryManager

**GetBlockChan Updated**:
- Returns `blockDelivery.BlockChan()` when BlockDeliveryManager is configured
- Returns `nil` otherwise (legacy mode or NoOpPropagator)

---

## Phase 8: Transition from Blocksync Reactor (IMPLEMENTED)

### Step 8.1: Disable blocksync reactor when propagation is used

When propagation + headersync handles syncing, simply don't add a blocksync reactor to the switch.
No NoOpReactor is needed - the blocksync reactor can be omitted entirely.

**Note**: The `SwitchToConsensus` interface is still needed for state sync, but that's handled
separately from blocksync.

### Step 8.2: Implement `IsCaughtUp` check (IMPLEMENTED)

Added `IsCaughtUp()` method to propagation reactor.

**File**: `consensus/propagation/reactor.go`

```go
func (r *Reactor) IsCaughtUp() bool {
    // Legacy mode - not applicable
    if r.pendingBlocks == nil || r.hsReader == nil {
        return false
    }

    // Check if headersync is caught up
    if !r.hsReader.IsCaughtUp() {
        return false
    }

    // Check if we have peers
    if len(r.getPeers()) == 0 {
        return false
    }

    // Check if all pending blocks are at or above consensus height
    lowestPending := r.pendingBlocks.LowestHeight()
    return lowestPending == 0 || lowestPending >= consensusHeight
}
```

**Reactor Options Added**:
- `WithHeaderSyncReader(reader)` - configures the HeaderSyncReader for IsCaughtUp checks

**Testing Criteria**:
- [x] Unit test: `TestIsCaughtUp_LegacyMode` - returns false when not configured (IMPLEMENTED)
- [x] Unit test: `TestIsCaughtUp_NoPeers` - returns false when no peers (IMPLEMENTED)
- [x] Unit test: `TestIsCaughtUp_HeaderSyncNotCaughtUp` - returns false when HS not caught up (IMPLEMENTED)
- [x] Unit test: `TestIsCaughtUp_PendingBlocksBelowConsensusHeight` - returns false (IMPLEMENTED)
- [x] Unit test: `TestIsCaughtUp_AllConditionsMet` - returns true when all conditions met (IMPLEMENTED)
- [x] Unit test: `TestIsCaughtUp_PendingBlocksAtConsensusHeight` - returns true (IMPLEMENTED)
- [x] Unit test: `TestIsCaughtUp_PendingBlocksAboveConsensusHeight` - returns true (IMPLEMENTED)
- [ ] Integration test: `TestCaughtUp_SwitchesToConsensus` - triggers consensus switch

---

## Phase 9: Full Integration Tests (PARTIALLY IMPLEMENTED)

### Step 9.1: Single node blocksync (IMPLEMENTED)

**File**: `consensus/propagation/blocksync_integration_test.go`

The following integration tests have been implemented:

1. **`TestSingleNodeBlocksync`** - Full blocksync workflow:
   - Creates 2 reactors (ahead and lagging)
   - Creates 5 blocks on the ahead node
   - Sets up PendingBlocksManager with MockHeaderSyncReader
   - Fills capacity from headers
   - Simulates receiving parts from ahead node
   - Verifies blocks are delivered in order via BlockDeliveryManager

2. **`TestSingleNodeBlocksync_OutOfOrderParts`** - Out-of-order handling:
   - Sends parts in reverse height order (height 3, 2, 1)
   - Verifies blocks are still delivered in correct order (1, 2, 3)
   - Tests BlockDeliveryManager's buffering logic

3. **`TestSingleNodeBlocksync_PartialProgress`** - Gap handling:
   - Completes block 1 first (delivered immediately)
   - Completes block 3 (buffered, waiting for block 2)
   - Completes block 2 (both 2 and 3 delivered in order)
   - Tests that gaps don't block earlier blocks

4. **`TestSingleNodeBlocksync_StateConsistency`** - State verification:
   - Verifies initial state (0 blocks, has capacity)
   - Verifies all blocks tracked after TryFillCapacity
   - Verifies all blocks marked Complete after receiving parts
   - Verifies Prune correctly removes old blocks

**Testing Criteria**:
- [x] Integration test: `TestSingleNodeBlocksync` - node catches up (IMPLEMENTED)
- [x] Verify: Blocks applied in strictly increasing order (IMPLEMENTED)
- [x] Verify: State is consistent after sync (IMPLEMENTED)

### Step 9.2: Multi-node parallel blocksync (IMPLEMENTED)

**File**: `consensus/propagation/blocksync_integration_test.go`

The following multi-node integration tests have been implemented:

1. **`TestMultiNodeParallelBlocksync`** - Parallel sync verification:
   - Creates 4 reactors (2 ahead, 2 lagging)
   - Creates 5 blocks on both ahead nodes
   - Sets up PendingBlocksManager for both lagging nodes
   - Simulates distributed part reception (even parts to lagging1, odd to lagging2)
   - Completes both nodes with remaining parts
   - Verifies both nodes receive all blocks in order

2. **`TestMultiNodeParallelBlocksync_DifferentSpeeds`** - Different sync speeds:
   - Creates 3 reactors (1 ahead, 1 fast lagging, 1 slow lagging)
   - Fast node receives all parts immediately
   - Slow node receives block 1, then later receives remaining blocks
   - Verifies both nodes sync correctly despite different speeds
   - Tests that independent node progress doesn't interfere

3. **`TestMultiNodeParallelBlocksync_SharedParts`** - Gossip simulation:
   - Creates 4 reactors (1 ahead, 3 lagging nodes)
   - Initially distributes parts across nodes (each gets 1/3 of parts)
   - Simulates gossip by sharing all parts to all nodes
   - Verifies HandlePart is idempotent (duplicates don't cause errors)
   - Verifies all 3 nodes receive all blocks in order

**Testing Criteria**:
- [x] Integration test: `TestMultiNodeParallelBlocksync` - multiple nodes sync (IMPLEMENTED)
- [x] Integration test: `TestMultiNodeParallelBlocksync_DifferentSpeeds` - different sync rates (IMPLEMENTED)
- [x] Integration test: `TestMultiNodeParallelBlocksync_SharedParts` - gossip simulation (IMPLEMENTED)
- [ ] Benchmark: Part-level parallelism faster than block-level (deferred - not required for functionality)

### Step 9.3: Blocksync + live consensus (IMPLEMENTED)

**File**: `consensus/propagation/blocksync_integration_test.go`

The following blocksync-to-live-consensus integration tests have been implemented:

1. **`TestBlocksyncWithLiveConsensus`** - Full transition workflow:
   - Network produces 5 initial blocks
   - Lagging node catches up via blocksync (verifies `IsCaughtUp` returns false during sync)
   - Receives all blocksync blocks in order via `blockChan`
   - Transitions to live consensus at height 6
   - Network produces block 6, node receives parts via `partChan` (live mode)
   - Verifies live block does NOT go through `blockChan`

2. **`TestBlocksyncToLiveTransition_NoPendingBlocks`** - Clean transition:
   - Syncs all blocks via blocksync
   - Verifies no pending blocks after sync
   - Verifies `IsCaughtUp` returns true
   - Verifies `LowestHeight()` returns 0 when empty

3. **`TestBlocksyncWithContinuousBlockProduction`** - Continuous production:
   - Network produces initial 3 blocks
   - Lagging node starts syncing
   - Network produces 2 more blocks while syncing
   - Lagging node refills capacity to get new headers
   - All 5 blocks delivered in order
   - Verifies state consistency after sync

**Testing Criteria**:
- [x] Integration test: `TestBlocksyncWithLiveConsensus` - full workflow (IMPLEMENTED)
- [x] Verify: No blocks missed during transition (IMPLEMENTED - blocks delivered in order)
- [x] Additional test: `TestBlocksyncToLiveTransition_NoPendingBlocks` - clean state after sync (IMPLEMENTED)
- [x] Additional test: `TestBlocksyncWithContinuousBlockProduction` - handles network progress (IMPLEMENTED)

### Step 9.4: Error recovery (IMPLEMENTED)

**File**: `consensus/propagation/blocksync_integration_test.go`

The following error recovery tests have been implemented:

1. **`TestBlocksyncErrorRecovery_PeerDisconnect`** - Peer disconnect recovery:
   - Creates 2 ahead peers and 1 lagging node
   - Receives partial parts from peer1 for blocks 1-3
   - Simulates peer1 disconnect (removes from peer list)
   - Peer2 provides remaining parts (including duplicates from overlap)
   - Verifies all 5 blocks delivered correctly despite peer disconnect

2. **`TestBlocksyncErrorRecovery_InvalidParts`** - Invalid parts rejection:
   - Tests corrupted data (random bytes with valid proof) - rejected
   - Tests wrong height (height 999 not tracked) - rejected
   - Tests out-of-range index (index 9999) - rejected
   - Verifies block state not corrupted after invalid parts
   - Verifies valid parts still complete the block

3. **`TestBlocksyncErrorRecovery_DuplicateParts`** - Duplicate handling:
   - Sends same part 3 times
   - Verifies first add returns true, subsequent adds return false
   - Verifies part count stays at 1 despite duplicates
   - Verifies block completes normally

4. **`TestBlocksyncErrorRecovery_StaleHeightParts`** - Pruned height handling:
   - Completes and prunes blocks 1 and 2
   - Attempts to send parts for pruned height 1
   - Verifies part rejected (added=false) without error
   - Verifies block 3 can still complete

5. **`TestBlocksyncErrorRecovery_MultipleSimultaneousFailures`** - Chaos testing:
   - 3 peers send parts chaotically: valid, duplicates, and corrupted
   - Interspersed pattern of errors and valid parts
   - Verifies all 5 blocks delivered correctly despite chaos

**Testing Criteria**:
- [x] Integration test: `TestBlocksyncErrorRecovery_PeerDisconnect` - recovers from peer failures (IMPLEMENTED)
- [x] Integration test: `TestBlocksyncErrorRecovery_InvalidParts` - rejects invalid parts (IMPLEMENTED)
- [x] Integration test: `TestBlocksyncErrorRecovery_DuplicateParts` - handles duplicates (IMPLEMENTED)
- [x] Integration test: `TestBlocksyncErrorRecovery_StaleHeightParts` - handles stale heights (IMPLEMENTED)
- [x] Integration test: `TestBlocksyncErrorRecovery_MultipleSimultaneousFailures` - handles chaos (IMPLEMENTED)
- [x] Verify: Invalid parts don't corrupt state (IMPLEMENTED - tested in InvalidParts test)
- [ ] Verify: Misbehaving peers banned (deferred - requires peer banning infrastructure)

---

## Implementation Order

1. **Phase 1** (Core data structures) - Foundation
2. **Phase 2** (Entry points) - Basic functionality
3. **Phase 3** (Part handling) - Core sync logic
4. **Phase 4** (Memory management) - Safety limits
5. **Phase 9.1** (Single node test) - **FIRST INTEGRATION TEST**
6. **Phase 5** (Headersync integration) - Connect to header stream
7. **Phase 6** (Ordered delivery) - **CRITICAL**: Consensus integration
8. **Phase 9.2-9.3** (Multi-node tests) - Verify parallel sync
9. **Phase 7** (Request coordination) - Optimize throughput
10. **Phase 8** (Blocksync transition) - Replace old reactor
11. **Phase 9.4** (Error recovery) - Robustness
12. **Phase 10** (Node integration) - Wire into node startup
13. **Phase 11** (E2E tests) - Full system validation

---

## Phase 10: Node Integration (IMPLEMENTED)

The `PendingBlocksManager` and `BlockDeliveryManager` are now created automatically when the propagation reactor is created, since they are required for the unified catchup/blocksync functionality.

### Step 10.1: Auto-create managers in NewReactor (IMPLEMENTED)

`NewReactor` now creates the managers internally before applying options, which allows options to override if needed:

**File**: `consensus/propagation/reactor.go`

```go
func NewReactor(self p2p.ID, cfg Config, options ...func(*Reactor)) *Reactor {
    // ... existing initialization ...

    // Always create the pending blocks manager for unified catchup/blocksync.
    // This can be overridden via WithPendingBlocksManager option if needed.
    pendingBlocks := NewPendingBlocksManager(
        nil, // logger set later via SetLogger
        config.Store,
        PendingBlocksConfig{}, // Uses defaults: MaxConcurrent=500, MemoryBudget=12GiB
    )
    reactor.pendingBlocks = pendingBlocks

    // Always create the block delivery manager for ordered block delivery.
    // startHeight=1 is a placeholder - it will be updated when we know the actual
    // last committed height during OnStart or via SetNextHeight.
    blockDelivery := NewBlockDeliveryManager(pendingBlocks, 1, nil)
    reactor.blockDelivery = blockDelivery

    // Apply options (can override defaults)
    for _, opt := range options {
        opt(reactor)
    }

    return reactor
}
```

**Testing Criteria**:
- [x] Unit test: `TestNewReactor_CreatesManagers` - verify managers created automatically (IMPLEMENTED)
- [x] Unit test: `TestNewReactor_ManagersConfigurable` - verify options can override defaults (IMPLEMENTED)

### Step 10.2: Wire HeaderSyncReader in node.go (IMPLEMENTED)

Modified `node/node.go` to connect the headersync reactor to the propagation reactor:

**File**: `node/node.go`

```go
if propagationReactor != nil {
    propagationReactor.SetLogger(logger.With("module", "propagation"))
    // Wire headersync reader to propagation reactor for unified blocksync
    if hsReactor != nil {
        propagationReactor.SetHeaderSyncReader(hsReactor)
    }
}
```

Added `SetHeaderSyncReader` method to reactor that also propagates to `PendingBlocksManager`:

**File**: `consensus/propagation/reactor.go`

```go
func (r *Reactor) SetHeaderSyncReader(reader HeaderSyncReader) {
    r.mtx.Lock()
    defer r.mtx.Unlock()
    r.hsReader = reader
    // Also set on the pending blocks manager for header fetching
    if r.pendingBlocks != nil {
        r.pendingBlocks.SetHeaderSyncReader(reader)
    }
}
```

**Testing Criteria**:
- [x] Verify: HeaderSyncReader connected when both reactors exist (IMPLEMENTED)
- [x] Unit test: `TestSetHeaderSyncReader_PropagatesReader` - verifies reader is set (IMPLEMENTED)

### Step 10.3: Start BlockDeliveryManager with reactor (IMPLEMENTED)

`OnStart` and `OnStop` now manage the BlockDeliveryManager lifecycle:

**File**: `consensus/propagation/reactor.go`

```go
func (blockProp *Reactor) OnStart() error {
    // Start the block delivery manager if configured
    if blockProp.blockDelivery != nil {
        blockProp.blockDelivery.Start()
    }
    return nil
}

func (blockProp *Reactor) OnStop() {
    blockProp.cancel()
    // Stop the block delivery manager if configured
    if blockProp.blockDelivery != nil {
        blockProp.blockDelivery.Stop()
    }
}
```

Also added `SetLogger` methods to both managers and propagation from reactor:

**File**: `consensus/propagation/pending_blocks.go`

```go
func (d *BlockDeliveryManager) SetLogger(logger log.Logger) { ... }
func (m *PendingBlocksManager) SetLogger(logger log.Logger) { ... }
```

**File**: `consensus/propagation/reactor.go`

```go
func (blockProp *Reactor) SetLogger(logger log.Logger) {
    blockProp.Logger = logger
    // Also set logger on the managers
    if blockProp.pendingBlocks != nil {
        blockProp.pendingBlocks.SetLogger(logger)
    }
    if blockProp.blockDelivery != nil {
        blockProp.blockDelivery.SetLogger(logger)
    }
}
```

**Testing Criteria**:
- [x] Unit test: `TestReactor_StartsBlockDelivery` - BlockDeliveryManager starts with reactor (IMPLEMENTED)
- [x] Unit test: `TestReactor_StopsBlockDelivery` - BlockDeliveryManager stops with reactor (IMPLEMENTED)
- [x] Unit test: `TestSetLogger_PropagatesLogger` - logger propagates to managers (IMPLEMENTED)
- [x] Unit test: `TestGetBlockChan_WithBlockDelivery` - GetBlockChan returns channel (IMPLEMENTED)
- [x] Unit test: `TestGetBlockChan_WithoutBlockDelivery` - GetBlockChan returns nil when disabled (IMPLEMENTED)

---

## Phase 11: E2E Tests (IMPLEMENTED)

Full end-to-end tests using the test harness to verify the complete system works.

### Step 11.1: E2E blocksync test (IMPLEMENTED)

**Files**:
- `test/e2e/networks/blocksync.toml` - E2E network manifest for blocksync testing
- `test/e2e/tests/blocksync_test.go` - E2E test suite for unified blocksync

**Implemented Tests**:
1. `TestE2E_BlocksyncToConsensus` - Verifies smooth transition from blocksync to live consensus
2. `TestE2E_BlocksyncBlockConsistency` - Verifies blocks synced via blocksync match authoritative chain
3. `TestE2E_IsCaughtUp` - Verifies IsCaughtUp returns true after sync

**Network Manifest** (`test/e2e/networks/blocksync.toml`):
- 4 validators that produce blocks from genesis
- `full01` starts at height 20 (tests blocksync from genesis)
- `full02` starts at height 40 with state sync (tests blocksync after state sync)
- `full03` starts at height 5 with perturbations (tests catchup after kill/restart)

**Testing Criteria**:
- [x] E2E test: Node syncs from genesis using unified blocksync (syncing from scract occurs in the CI e2e test)
- [x] E2E test: Node transitions from blocksync to live consensus (TestE2E_BlocksyncToConsensus)
- [x] E2E test: Verify IsCaughtUp returns true after sync (TestE2E_IsCaughtUp)

### Step 11.2: E2E catchup test (IMPLEMENTED)

**Implemented Tests**:
1. `TestE2E_UnifiedCatchup` - Verifies nodes catch up after perturbations (kill/disconnect/restart)
2. `TestE2E_CatchupPartProgress` - Verifies all nodes stay within a few blocks of each other

**Testing Criteria**:
- [x] E2E test: Partitioned node catches up after reconnection (TestE2E_UnifiedCatchup)
- [x] E2E test: Catchup uses part-level parallelism (verified via TestE2E_CatchupPartProgress)

### Step 11.3: E2E mixed scenario test (IMPLEMENTED)

**Implemented Tests**:
1. `TestE2E_BlocksyncThenCatchup` - Tests full lifecycle: blocksync → live consensus → perturbation → catchup
2. `TestE2E_NoStateCorruption` - Verifies no state corruption across transitions

**Testing Criteria**:
- [x] E2E test: Full lifecycle from sync to catchup works (TestE2E_BlocksyncThenCatchup)
- [x] E2E test: No state corruption across transitions (TestE2E_NoStateCorruption)

### How to Run E2E Tests

To run the blocksync e2e tests:

```bash
# Build the test app
cd test/e2e && make app

# Generate and start the network
./build/runner -f networks/blocksync.toml setup
./build/runner -f networks/blocksync.toml start

# Run the tests
E2E_MANIFEST=networks/blocksync.toml go test -v ./tests/... -run TestE2E

# Clean up
./build/runner -f networks/blocksync.toml stop
./build/runner -f networks/blocksync.toml cleanup
```

---

## Critical Invariants

1. **Height ordering for blocksync**: Complete blocksync blocks MUST be delivered to consensus in strictly increasing height order. The `BlockDeliveryManager` enforces this by buffering out-of-order completions. Violation causes state corruption.

2. **Live consensus parts are immediate**: Parts for the current consensus height flow through `partChan` immediately - no ordering required. Consensus accumulates them in `ProposalBlockParts`.

3. **Dual routing for current height**: Parts at the current consensus height go to BOTH `partChan` (for live consensus) AND `PendingBlocksManager` (for state tracking). This ensures the manager always has complete state.

4. **Single writer per mode**:
   - Live consensus: `syncData` sends to `peerMsgQueue` → consensus state machine handles
   - Blocksync: `syncData` calls `applyBlocksyncBlock` directly

5. **Trust model for parts**: Parts can be accepted from three trust sources:
   - **CompactBlock**: Proof cache from proposer-signed compact block (no inline proof needed)
   - **Verified header**: BlockID from headersync (inline proof required, verified against PartSetHeader)
   - **Commitment**: PartSetHeader from consensus votes (inline proof required, verified against PSH)

6. **Commitment bypass**: Blocks from commitments MUST be accepted regardless of capacity limits. Rejecting them would halt consensus.

7. **Prune after commit**: Pending block state MUST be pruned after commit to prevent unbounded memory growth.

8. **Proof required for catchup**: Parts without CompactBlock MUST have Merkle proofs attached (`Prove=true`).

9. **Height boundary consistency**: The `height` field in propagation reactor tracks the CURRENT consensus height. Parts with `height < blockProp.height` are blocksync. Parts with `height == blockProp.height` are live consensus.

---

## State Transitions and Mode Switching

### During Blocksync (node is behind)

```
State: height = H, consensus not yet started

headersync verifies H, H+1, H+2, ...
     │
     ▼
PendingBlocksManager tracks H, H+1, H+2, ...
     │
     ▼
Parts downloaded for each height
     │
     ▼
BlockDeliveryManager delivers H → consensus
     │
     ▼
applyBlocksyncBlock(H) → state advances to H
     │
     ▼
Prune(H) → PendingBlocksManager removes H
     │
     ▼
BlockDeliveryManager delivers H+1 → consensus
     │
     ▼
... repeat until caught up ...
```

### Transition to Live Consensus

```
Blocksync catches up to peer heights
     │
     ▼
IsCaughtUp() returns true
     │
     ▼
SwitchToConsensus called (similar to current blocksync reactor)
     │
     ▼
propagator.SetHeightAndRound(H+1, 0)
     │
     ▼
propagator.StartProcessing()
     │
     ▼
Now: parts for H+1 go through partChan (live mode)
```

### During Live Consensus

```
State: height = H, consensus running

CompactBlock received for H
     │
     ├─► proposalChan → consensus setProposal()
     │
     ▼
Parts received for H
     │
     ├─► PendingBlocksManager.HandlePart(H, ...)
     │
     ├─► partChan → consensus addProposalBlockPart()
     │
     ▼
All parts complete → consensus finalizes H
     │
     ▼
propagator.Prune(H)
propagator.SetHeightAndRound(H+1, 0)
     │
     ▼
Now tracking H+1
```

### If Node Falls Behind During Live Consensus

```
Consensus at H, but learns of commit at H+3
     │
     ▼
AddCommitment(H+3, round, psh) called
     │
     ▼
PendingBlocksManager tracks H+3 (catchup mode)
     │
     ▼
Meanwhile, headersync verifies H+1, H+2, H+3
     │
     ▼
AddFromHeader(H+1), AddFromHeader(H+2), AddFromHeader(H+3)
     │
     ▼
Parts downloaded for H+1, H+2, H+3
     │
     ▼
BlockDeliveryManager delivers H+1 (via blockChan)
     │
     ▼
... continues as blocksync until caught up ...
```

### Slow Proposal Scenario (Race Between Entry Points)

This is a key scenario: the node is at height H, waiting for a proposal that's slow to arrive. Multiple entry points may fire:

```
Node at height H, expecting proposal
     │
     │ Time passes... proposal is slow
     │
     ├─► Scenario A: CompactBlock arrives first
     │        │
     │        ▼
     │   AddProposal(cb) → parts download via have/want
     │        │
     │        ▼
     │   Parts → partChan → consensus finalizes H
     │
     ├─► Scenario B: Headersync gets header first (network moved on)
     │        │
     │        ▼
     │   AddFromHeader(H) → creates PendingBlock
     │        │
     │        ▼
     │   Parts requested with Prove=true (no CompactBlock yet)
     │        │
     │        ├─► Later: CompactBlock arrives
     │        │        │
     │        │        ▼
     │        │   Attaches to existing PendingBlock
     │        │   Validates BlockID matches header's BlockID
     │        │   Now has proof cache - future parts don't need inline proofs
     │        │
     │        ▼
     │   Block completes → parts via partChan (still live height)
     │
     └─► Scenario C: Consensus gets commit first (fast network)
              │
              ▼
         AddCommitment(H, round, psh) → creates PendingBlock
              │
              ▼
         Parts requested with Prove=true
              │
              ├─► Later: CompactBlock arrives
              │        │
              │        ▼
              │   Attaches to existing PendingBlock
              │   Validates BlockID.PartSetHeader matches commitment's PSH
              │
              ├─► Later: Header arrives (from headersync)
              │        │
              │        ▼
              │   Upgrades PendingBlock with full BlockID
              │   Now has verified header for blocksync path
              │
              ▼
         Block completes → parts via partChan (still live height)
```

**Key insight**: The `PendingBlock` can be created by ANY entry point and then upgraded/enriched by later arrivals:
- Commitment → can be upgraded by Header → can be enriched by CompactBlock
- Header → can be enriched by CompactBlock
- CompactBlock → can be validated against existing Header

---

## Success Metrics

1. **Sync throughput**: Should achieve >1 blocks/second on mainnet
2. **Memory usage**: Should stay under 12GiB budget
3. **No ordering violations**: Zero instances of out-of-order block application
