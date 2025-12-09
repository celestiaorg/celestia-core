# ADR 012: Header Sync Reactor

## Changelog

- 2024-12-09: Initial draft

## Context

The current node architecture has two mechanisms for catching up to the chain head:

1. **Blocksync reactor**: Downloads complete blocks sequentially, verifying each before proceeding to the next.
2. **Propagation reactor**: Downloads a single block in parallel from multiple peers using erasure-coded parts.

Neither mechanism is optimal for catching up from a significant height deficit:

- Blocksync is sequential and cannot leverage parallel downloads for multiple blocks
- Propagation reactor cannot verify blocks ahead of time because it doesn't know the block commitment until it receives the block proposal

If a node could verify headers ahead of block data, it would know:
- The expected block hash (commitment)
- The PartSetHeader (total parts and Merkle root)
- The block size

This information enables downloading multiple blocks in parallel with confidence, as the propagation reactor can verify each part against the known PartSetHeader.

### The BlockMeta Assumption Problem

Currently, the codebase assumes that if `LoadBlockMeta(height)` returns non-nil, then the full block exists at that height. This assumption exists in:

- `store.LoadBlock()` - loads BlockMeta first, then parts
- `store.LoadPartSet()` - loads BlockMeta for part count
- `rpc/core/blocks.go` - uses BlockMeta to determine block availability
- `consensus/reactor.go` - uses BlockMeta height as "has block" indicator

With header-first sync, we will have BlockMeta (which contains the Header) before we have block parts. Every location that assumes "BlockMeta exists ⟹ block exists" must be audited and potentially modified.

## Alternative Approaches

### Alternative 1: Extend Blocksync with Parallel Block Downloads

We could modify blocksync to download multiple blocks in parallel.

**Rejected because:**
- Still requires downloading full blocks before verification
- Cannot leverage the propagation reactor's erasure coding advantages
- Duplicates effort rather than composing existing components

### Alternative 2: Light Client-Style Header Sync

We could implement full light client verification with skipping (trusting intermediate headers based on 1/3 trust threshold).

**Rejected because:**
- More complex than necessary for full nodes
- Full nodes have the state store with validator sets; no need for skipping verification
- Adds latency due to bisection when validators change frequently

### Alternative 3: Modify Propagation Reactor Only

We could add header-fetching logic directly to the propagation reactor.

**Rejected because:**
- Mixes concerns (header sync vs block propagation)
- Harder to test and reason about
- Propagation reactor is already complex

## Decision

Implement a new **Header Sync Reactor** that:

1. Syncs headers ahead of blocks using the same peer-discovery and request patterns as blocksync
2. Verifies headers using commits and validator sets from the state store
3. Stores verified headers/BlockMeta without requiring block parts
4. Publishes verified headers to subscribers (e.g., propagation reactor)
5. Maintains a clear separation between "header height" and "block height"

The propagation reactor will subscribe to header sync and use the known commitments to download blocks in parallel.

## Detailed Design

### New Package Structure

```
headersync/
├── reactor.go       # Main reactor implementation
├── pool.go          # Peer and request management (similar to blocksync/pool.go)
├── msgs.go          # Message type definitions
├── metrics.go       # Prometheus metrics
└── reactor_test.go  # Tests
```

### Proto Messages

```protobuf
// proto/tendermint/headersync/types.proto

// StatusResponse contains a peer's header sync status.
// Sent proactively when a peer connects or when header height advances.
// Peers MUST NOT send status updates with the same or lower height as previously
// reported - doing so results in disconnection (DoS protection).
message StatusResponse {
  int64 base = 1;
  int64 height = 2;
}

// SignedHeader pairs a header with its commit
message SignedHeader {
  tendermint.types.Header header = 1;
  tendermint.types.Commit commit = 2;
}

// GetHeaders requests a batch of headers starting from start_height
message GetHeaders {
  int64 start_height = 1;
  int64 count = 2;  // max 50
}

// HeadersResponse is the response containing sequential signed headers
// An empty array indicates the peer has no headers from start_height
message HeadersResponse {
  repeated SignedHeader headers = 1;
}

message Message {
  oneof sum {
    StatusResponse status_response = 1;
    GetHeaders get_headers = 2;
    HeadersResponse headers_response = 3;
  }
}
```

The protocol uses only 3 message types:
- `StatusResponse`: Push-based peer status updates (no request message needed)
- `GetHeaders` / `HeadersResponse`: Batch header fetching

This is simpler than blocksync's 5-message protocol.

**Push-Based Status Updates**: Unlike blocksync which polls peers for status, header sync uses push-based status updates. Peers broadcast their status when their header height advances, saving a round trip. To prevent DoS attacks from peers spamming status updates, peers that send updates with the same or lower height than previously reported are disconnected and banned.

**Initial Status on Peer Connection**: When a new peer connects, nodes send their status with `height - 1` as a safety margin. This prevents triggering DoS protection if the node's height advances immediately after the peer connects and a status broadcast occurs (which would otherwise send two updates with close heights to the same peer).

### Channel Configuration

```go
const (
    HeaderSyncChannel = byte(0x60)  // New channel ID, distinct from blocksync (0x40)
)

func (r *Reactor) GetChannels() []*p2p.ChannelDescriptor {
    return []*p2p.ChannelDescriptor{
        {
            ID:                  HeaderSyncChannel,
            Priority:            25,  // Slightly lower than blocksync
            SendQueueCapacity:   1000,
            RecvBufferCapacity:  50 * 1024,  // Headers are small
            RecvMessageCapacity: MaxMsgSize,
            MessageType:         &hsproto.Message{},
        },
    }
}
```

### Core Types

```go
const (
    // MaxHeaderBatchSize is the maximum number of headers per request
    MaxHeaderBatchSize = 50
)

// HeaderBatchRequest represents a request for a range of headers
type HeaderBatchRequest struct {
    StartHeight int64
    Count       int64
    PeerID      p2p.ID
}

// HeaderPool manages peer connections and header requests
type HeaderPool struct {
    service.BaseService

    mtx           sync.Mutex
    peers         map[p2p.ID]*hsPeer
    pendingBatches map[int64]*headerBatch  // startHeight -> batch
    height        int64  // Next header height to request
    maxPeerHeight int64  // Highest header height reported by any peer

    // Channels for communication
    requestsCh    chan<- HeaderBatchRequest
    errorsCh      chan<- peerError
}

// headerBatch tracks a pending batch request
type headerBatch struct {
    startHeight int64
    count       int64
    peerID      p2p.ID
    requestTime time.Time
    headers     []*SignedHeader  // filled in as response arrives
}

// SignedHeader pairs a header with its commit (mirrors proto)
type SignedHeader struct {
    Header *types.Header
    Commit *types.Commit
}

// VerifiedHeader contains a header that has been fully verified
type VerifiedHeader struct {
    Header   *types.Header
    Commit   *types.Commit
    BlockID  types.BlockID
}

// Reactor coordinates header synchronization
type Reactor struct {
    p2p.BaseReactor

    pool        *HeaderPool
    stateStore  sm.Store      // For loading validator sets
    blockStore  sm.BlockStore // For storing headers and checking existing data
    chainID     string

    // Subscriber management
    subscribersMtx sync.RWMutex
    subscribers    []chan<- *VerifiedHeader

    metrics *Metrics
}
```

### Verification Logic

```go
func (r *Reactor) verifyHeader(
    header *types.Header,
    commit *types.Commit,
    prevHeader *types.Header,
) error {
    // 1. Load validator set for this height
    vals, err := r.stateStore.LoadValidators(header.Height)
    if err != nil {
        return fmt.Errorf("failed to load validators at height %d: %w", header.Height, err)
    }

    // 2. Compute expected BlockID from header
    blockID := types.BlockID{
        Hash: header.Hash(),
        // Note: PartSetHeader comes from the commit, not computed here
        // This is a key difference - we trust the PartSetHeader from
        // the commit that was signed by +2/3 validators
        PartSetHeader: commit.BlockID.PartSetHeader,
    }

    // 3. Verify commit has +2/3 voting power
    err = vals.VerifyCommitLight(
        r.chainID,
        blockID,
        header.Height,
        commit,
    )
    if err != nil {
        return fmt.Errorf("commit verification failed: %w", err)
    }

    // 4. Verify chain linkage (if not first header)
    if prevHeader != nil {
        // Check LastBlockID matches previous header
        prevBlockID := types.BlockID{
            Hash:          prevHeader.Hash(),
            PartSetHeader: /* loaded from stored commit */,
        }
        if !header.LastBlockID.Equals(prevBlockID) {
            return fmt.Errorf("header LastBlockID mismatch")
        }

        // Check ValidatorsHash continuity
        if !bytes.Equal(header.ValidatorsHash, prevHeader.NextValidatorsHash) {
            return fmt.Errorf("validators hash mismatch")
        }
    }

    // 5. Verify header.ValidatorsHash matches loaded validators
    if !bytes.Equal(header.ValidatorsHash, vals.Hash()) {
        return fmt.Errorf("header validators hash does not match state store")
    }

    return nil
}
```

### Storage Changes

Add new methods to the BlockStore interface:

```go
type BlockStore interface {
    // ... existing methods ...

    // SaveHeader saves a verified header and commit without block parts.
    // This creates a BlockMeta entry but does NOT update the contiguous block height.
    SaveHeader(header *types.Header, commit *types.Commit) error

    // HeaderHeight returns the highest height for which we have a verified header.
    // This may be greater than Height() if headers are synced ahead of blocks.
    HeaderHeight() int64

    // HasHeader returns true if we have a verified header at the given height.
    HasHeader(height int64) bool

    // LoadHeader returns the header at the given height, or nil if not found.
    // This may return a header even when LoadBlock returns nil.
    LoadHeader(height int64) *types.Header
}
```

Implementation in `store/store.go`:

```go
// New key for tracking header height separately
func calcHeaderHeightKey() []byte {
    return []byte("headerHeight")
}

func (bs *BlockStore) SaveHeader(header *types.Header, commit *types.Commit) error {
    batch := bs.db.NewBatch()
    defer batch.Close()

    height := header.Height

    // Create BlockMeta with zero-size (no block parts yet)
    // The PartSetHeader comes from the commit's BlockID
    blockMeta := &types.BlockMeta{
        BlockID: types.BlockID{
            Hash:          header.Hash(),
            PartSetHeader: commit.BlockID.PartSetHeader,
        },
        BlockSize: 0,  // Unknown until block is downloaded
        Header:    *header,
        NumTxs:    0,  // Unknown until block is downloaded
    }

    // Save BlockMeta
    metaBytes := mustEncode(blockMeta.ToProto())
    if err := batch.Set(calcBlockMetaKey(height), metaBytes); err != nil {
        return err
    }

    // Save hash -> height mapping
    if err := batch.Set(calcBlockHashKey(header.Hash()), []byte(fmt.Sprintf("%d", height))); err != nil {
        return err
    }

    // Save commit
    commitBytes := mustEncode(commit.ToProto())
    if err := batch.Set(calcSeenCommitKey(height), commitBytes); err != nil {
        return err
    }

    // Update header height if this is higher
    bs.mtx.Lock()
    defer bs.mtx.Unlock()

    if height > bs.headerHeight {
        bs.headerHeight = height
        headerHeightBytes := make([]byte, 8)
        binary.BigEndian.PutUint64(headerHeightBytes, uint64(height))
        if err := batch.Set(calcHeaderHeightKey(), headerHeightBytes); err != nil {
            return err
        }
    }

    return batch.WriteSync()
}
```

### Handling the BlockMeta Assumption

Locations that need modification:

1. **`store.LoadBlock()`** - Already handles missing parts gracefully (returns nil)

2. **`store.LoadPartSet()`** - Already returns error if parts missing

3. **`rpc/core/blocks.go`**:
```go
// Before: assumed block exists if meta exists
// After: check if block parts exist
func Block(ctx *rpctypes.Context, heightPtr *int64) (*ctypes.ResultBlock, error) {
    // ...
    block := env.BlockStore.LoadBlock(height)
    if block == nil {
        // Could have header but not block
        if meta := env.BlockStore.LoadBlockMeta(height); meta != nil {
            return nil, fmt.Errorf("block %d: header available but block data not yet synced", height)
        }
        return nil, fmt.Errorf("block %d not found", height)
    }
    // ...
}
```

4. **`blocksync/reactor.go`** - Uses `store.Height()` which tracks full blocks, not headers. No change needed.

5. **`consensus/reactor.go`** - Uses BlockMeta to respond to peer requests. Should check for block availability:
```go
func (r *Reactor) respondToPeer(msg *bcproto.BlockRequest, src p2p.Peer) {
    block := r.store.LoadBlock(msg.Height)
    if block == nil {
        // Don't have block data (might have header only)
        return src.TrySend(p2p.Envelope{
            ChannelID: BlocksyncChannel,
            Message:   &bcproto.NoBlockResponse{Height: msg.Height},
        })
    }
    // ... respond with block
}
```

### Subscriber Interface

```go
// Subscribe registers a channel to receive verified headers.
// The channel should have sufficient buffer to avoid blocking.
func (r *Reactor) Subscribe(ch chan<- *VerifiedHeader) {
    r.subscribersMtx.Lock()
    defer r.subscribersMtx.Unlock()
    r.subscribers = append(r.subscribers, ch)
}

// Unsubscribe removes a subscriber channel.
func (r *Reactor) Unsubscribe(ch chan<- *VerifiedHeader) {
    r.subscribersMtx.Lock()
    defer r.subscribersMtx.Unlock()
    for i, sub := range r.subscribers {
        if sub == ch {
            r.subscribers = append(r.subscribers[:i], r.subscribers[i+1:]...)
            return
        }
    }
}

// notifySubscribers sends a verified header to all subscribers.
// Non-blocking: if a subscriber's channel is full, it is skipped.
func (r *Reactor) notifySubscribers(vh *VerifiedHeader) {
    r.subscribersMtx.RLock()
    defer r.subscribersMtx.RUnlock()

    for _, ch := range r.subscribers {
        select {
        case ch <- vh:
        default:
            r.Logger.Warn("Subscriber channel full, skipping notification",
                "height", vh.Header.Height)
        }
    }
}
```

### Metrics

```go
type Metrics struct {
    // Height of the last synced header
    HeaderHeight metrics.Gauge

    // Number of headers synced
    HeadersSynced metrics.Counter

    // Number of pending header requests
    PendingRequests metrics.Gauge

    // Number of connected peers with headers
    Peers metrics.Gauge

    // Header sync rate (headers per second)
    SyncRate metrics.Gauge

    // Verification failures
    VerificationFailures metrics.Counter
}
```

### Sync Algorithm

The sync loop uses push-based status updates (peers broadcast when height changes) rather than polling:

```go
func (r *Reactor) poolRoutine() {
    trySyncTicker := time.NewTicker(trySyncIntervalMS * time.Millisecond)
    defer trySyncTicker.Stop()

    lastBroadcastHeight := r.blockStore.HeaderHeight()

    for {
        select {
        case <-r.Quit():
            return

        case request := <-r.requestsCh:
            // Send batch header request to peer
            r.sendGetHeaders(request)

        case err := <-r.errorsCh:
            // Handle peer error (timeout, invalid data)
            r.handlePeerError(err)

        case <-trySyncTicker.C:
            // Try to process completed batch responses
            r.processCompletedBatches()

            // Broadcast our status if height has advanced
            // (push-based model saves a round trip vs polling)
            currentHeight := r.blockStore.HeaderHeight()
            if currentHeight > lastBroadcastHeight {
                r.BroadcastStatus()
                lastBroadcastHeight = currentHeight
            }
        }
    }
}

// processCompletedBatches verifies and stores headers from completed batch requests
func (r *Reactor) processCompletedBatches() {
    for {
        // Get next batch of headers ready for processing (in height order)
        batch, peer := r.pool.PeekCompletedBatch()
        if batch == nil {
            return
        }

        // Verify all headers in the batch sequentially
        allValid := true
        for _, sh := range batch.headers {
            if err := r.verifyHeader(sh.Header, sh.Commit, r.lastHeader); err != nil {
                r.Logger.Error("Header verification failed",
                    "height", sh.Header.Height,
                    "peer", peer,
                    "err", err)
                r.pool.RedoBatch(batch.startHeight)
                r.Switch.StopPeerForError(peer, err)
                r.metrics.VerificationFailures.Add(1)
                allValid = false
                break
            }

            // Store header
            if err := r.blockStore.SaveHeader(sh.Header, sh.Commit); err != nil {
                r.Logger.Error("Failed to save header", "err", err)
                allValid = false
                break
            }

            // Notify subscribers
            vh := &VerifiedHeader{
                Header:  sh.Header,
                Commit:  sh.Commit,
                BlockID: types.BlockID{
                    Hash:          sh.Header.Hash(),
                    PartSetHeader: sh.Commit.BlockID.PartSetHeader,
                },
            }
            r.notifySubscribers(vh)

            r.lastHeader = sh.Header
            r.metrics.HeaderHeight.Set(float64(sh.Header.Height))
            r.metrics.HeadersSynced.Add(1)
        }

        if allValid {
            r.pool.PopBatch(batch.startHeight)
        } else {
            break
        }
    }
}

// sendGetHeaders sends a GetHeaders request to a peer
func (r *Reactor) sendGetHeaders(req HeaderBatchRequest) {
    peer := r.Switch.Peers().Get(req.PeerID)
    if peer == nil {
        return
    }

    r.Logger.Debug("Requesting headers",
        "peer", req.PeerID,
        "start", req.StartHeight,
        "count", req.Count)

    peer.TrySend(p2p.Envelope{
        ChannelID: HeaderSyncChannel,
        Message: &hsproto.GetHeaders{
            StartHeight: req.StartHeight,
            Count:       req.Count,
        },
    })
}

// Receive handles incoming messages
func (r *Reactor) Receive(e p2p.Envelope) {
    switch msg := e.Message.(type) {
    case *hsproto.StatusResponse:
        // SetPeerRange returns false if this is a DoS attempt (same or lower height)
        if !r.pool.SetPeerRange(e.Src.ID(), msg.Base, msg.Height) {
            r.Switch.StopPeerForError(e.Src, errors.New("status update with non-increasing height"))
        }

    case *hsproto.GetHeaders:
        r.respondGetHeaders(msg, e.Src)

    case *hsproto.HeadersResponse:
        r.pool.AddBatchResponse(e.Src.ID(), msg.Headers)
    }
}

// respondGetHeaders responds to a GetHeaders request
func (r *Reactor) respondGetHeaders(msg *hsproto.GetHeaders, src p2p.Peer) {
    count := msg.Count
    if count > MaxHeaderBatchSize {
        count = MaxHeaderBatchSize
    }

    headers := make([]*hsproto.SignedHeader, 0, count)
    for h := msg.StartHeight; h < msg.StartHeight+count; h++ {
        meta := r.blockStore.LoadBlockMeta(h)
        if meta == nil {
            break  // Return what we have
        }
        commit := r.blockStore.LoadSeenCommit(h)
        if commit == nil {
            commit = r.blockStore.LoadBlockCommit(h)
        }
        if commit == nil {
            break
        }
        headers = append(headers, &hsproto.SignedHeader{
            Header: meta.Header.ToProto(),
            Commit: commit.ToProto(),
        })
    }

    src.TrySend(p2p.Envelope{
        ChannelID: HeaderSyncChannel,
        Message:   &hsproto.Headers{Headers: headers},
    })
}
```

### Testing Strategy

1. **Unit Tests**:
   - Header verification with valid/invalid commits
   - Chain linkage verification
   - BlockStore SaveHeader and HeaderHeight
   - Subscriber notification

2. **Integration Tests**:
   - Multi-peer header sync
   - Handling of slow/malicious peers
   - Recovery from network partitions
   - Interaction with existing reactors

3. **E2E Tests**:
   - Full node catch-up using header sync + propagation
   - Network with mixed header-sync and non-header-sync nodes

4. **Fuzz Tests**:
   - Random header/commit combinations
   - Malformed messages

### Configuration

```toml
[headersync]
# Enable header sync reactor
enabled = true

# Maximum headers per batch request (max 50)
batch_size = 50

# Maximum concurrent batch requests
max_pending_batches = 10

# Request timeout for a batch
request_timeout = "15s"

# Minimum peers before starting sync
min_peers = 2
```

## Status

Proposed

## Consequences

### Positive

- Enables parallel block download with known commitments
- Reduces catch-up time significantly for nodes far behind
- Clean separation of concerns (header sync vs block sync vs propagation)
- Reuses proven patterns from blocksync
- Headers are small; sync is network-efficient

### Negative

- Adds a new reactor and complexity to the system
- Requires auditing and modifying code that assumes BlockMeta implies block existence
- Additional storage overhead for header height tracking
- Subscribers must handle out-of-order block completion

### Neutral

- Does not change consensus protocol
- Does not require network upgrade (new channel, backwards compatible)
- Propagation reactor changes are separate (future work)

## References

- Blocksync reactor implementation: `blocksync/reactor.go`, `blocksync/pool.go`
- Propagation reactor: `consensus/propagation/`
- Light client verification: `types/validator_set.go` (VerifyCommitLight)
- BlockStore implementation: `store/store.go`
- ADR-001 Block Propagation: `docs/celestia-architecture/adr-001-block-propagation.md`
