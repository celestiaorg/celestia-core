# ADR 012: Sequence-Based Gossip in CAT

## Changelog

- 2026-01-21: proposed

## Context

The previous pendingSeenTracker cache could reach capacity and start dropping the
latest arrivals, creating sequence gaps inside the cache. Those gaps made it
hard for peers to request transactions reliably resulting in slowdowns and stalls.

Cat gossip is moving from txKey-based have message signaling to using a combined sequence+signer key model, since nodes already track that information locally. This lets peers maintain a compact, bounded-memory view of what each peer knows. Nodes gossip their observed
sequence ranges per signer, and we can optimistically request missing
transactions based on those advertised ranges, even if we have not seen the
transaction yet.

## Status

Proposed

## Alternative Approaches

Keep the hash-first design and add sequence/signer as a secondary path. Not
chosen because it leaves sequence-first logic scattered across components and
keeps the old mental model as the default.

## Decision

Make the sequence+signer model the primary path across the gossip stack. All new
logic and interfaces should work with `(signer, sequence)` as the primary
mechanism. Legacy txKey-based paths are kept for compatibility
that is explicitly deprecated and will be removed once all peers upgrade.

**Key principle**: Sequence-first design means interfaces operate primarily with
`string(signer, sequence)` as the tx identifier;

## Detailed Design

### Protocol Components

**Sequence Tracker** (sequence range based):

- Stores only per-peer signer, sequence ranges `[min, max]`.
- Updates ranges when `SeenTx` with min/max is received.
- Cleanup: Remove entries when peers disconnect. Optionally clean up stale signer
  entries (exact mechanism TBD, but should be bounded).

**TxKey Tracker** (txKey-based peers who did not upgrade):

- Two-way map used only for txKey-based peers:
    - `txKey -> (signer, sequence)` for deciding whether to buffer of CheckTx incoming txs.
    - `(signer, sequence) -> txKey` for sending WantTx to txKey-based peers.
- **Cleanup**: Remove entries on request completion, or peer
  disconnect.
- **Temporary**: Remove after all peers upgrade.

**Request Scheduler**:

- Primary interface works with `(signer, sequence)` keys.
- Internally tracks requests by `string(signer+sequence)` for upgraded peers.
- Legacy path: Also tracks by `txKey` when interacting with legacy peers.
- Current and legacy requests are tracked in separate states.

**Upgraded Peers Tracker**:

- Dedicated map/set: `upgradedPeers map[uint16]struct{}`
- Tracks which peers support sequence-based gossip.
- Used to route requests to the compatible peers

### Upgraded Peer Detection


- A peer is considered upgraded if it sends a `SeenTx` message
  with non-empty `min_sequence` and `max_sequence` fields.
- Once detected, the peer is cached and all subsequent messaging uses the
  sequence-based format.

**Tracking Upgraded Peers**:

- Maintain a dedicated `upgradedPeers` map/set to track which peers have upgraded
  to support sequence-based gossip.
- This map is checked before sending any `WantTx` or formatting `SeenTx` messages.
- When a peer disconnects, remove it from the upgraded peers map.
- Format selection: When sending messages, check the `upgradedPeers` map to determine
  which fields to populate (see Protocol Semantics above). The selection logic should
  be explicit and centralized to avoid mixing logic.


### Data Structures

```go
// Peer-scoped ranges by signer. Simplified to only track ranges, not individual txs.
type sequenceTracker struct {
  // peerID -> signerKey -> range
  // Compact representation: only min/max per peer-signer pair
  ranges map[uint16]map[string]seqRange
}

type seqRange struct {
  min uint64  // Lower bound of sequences this peer can serve
  max uint64  // Highest sequence this peer has seen
}

// Only populated when receiving haves from legacy peers
type TxKeyTracker struct {
  // Used when requesting from legacy peers: we know (signer, seq) but need txKey
  // Populated from SeenTx messages received from legacy peers
  signerSequenceToTxKey map[string]types.TxKey

  // Used when receiving Tx to decide whether to CheckTx or buffer
  // Populated from SeenTx messages received from legacy peers
  txKeyToSeqSigner map[types.TxKey]string
}

// Request scheduler request tracking
type requestScheduler struct {
  mu sync.Mutex
  // Primary: sequence-based requests (upgraded peers)
  bySignerSequence map[string]uint16  // string(signer+sequence) -> peerID

  // Legacy: txKey-based requests (only for legacy peers)
  byTxKey map[types.TxKey]uint16
}

// Tracks which peers support sequence-based gossip
type upgradedPeers struct {
  peers map[uint16]struct{}
}
```


### Inbound Flow

1. `SeenTx` received
      - If min_sequence and max_sequence are present: mark peer upgraded and
        update (peer, signer) ranges.
      - Otherwise: treat as legacy have; record txKey as well as signer/sequence.
2. `Tx` received
      - Upgraded: resolve by (signer, sequence) directly from parallel arrays in the message.
      - Legacy: resolve via `txKeyToSeqSigner`; if missing, treat as untracked.
3. `WantTx` received
      - Upgraded: expect (signer, sequence).
      - Legacy: expect txKey.

### Outbound Flow

1. `SeenTx` sent
      - Upgraded peer: include signer, sequence, min_sequence (and max_sequence if used).
      - Legacy peer: include txKey, signer, sequence, no min/max.
2. `WantTx` sent
      - Upgraded peer: send (signer, sequence).
      - Legacy peer: send txKey (resolved via signerSequenceToTxKey).
3. `Tx` sent
      - Upgraded peer: include signers and sequences.
      - Legacy peer: omit signers/sequences.

## Backwards Compatibility

- Legacy peers continue to gossip using `txKey`-based `SeenTx` and `WantTx` messages.

## Testing

**Performance/Monitoring Tests**:

- All upgraded peers: Measure improvement in transaction distribution latency
  and throughput using talis and latency monitor under load.
- Mixed network: Verify legacy peers still receive transactions correctly,
  no degradation in their experience.

## Consequences

### Positive

- Fixes sequence gaps that caused slowdowns and stalls.
- Bounded memory: compact view of peer states (ranges, not individual txs).
- Optimistic requests: can request by sequence before seeing the tx.

### Negative

- **Tech debt**: The support for two types of gossip mechanisms adds
  complexity that must be maintained during the transition period.
  However, this should be temporary.

### Areas for Improvement

1. **Specify sequence tracker cleanup mechanism**: Add a concrete proposal for stale signer     cleanup(e.g., "Remove signer entries that haven't been updated in the last N blocks
   and have no active requests, bounded to at most M signers per peer").

1. **Range update semantics**: Specify exactly how ranges are updated:
   - `max_sequence`: Always update to maximum seen
   - `min_sequence`: Update when peer advertises a new minimum (they've evicted
     older sequences)
   - Handle reductions in `max_sequence`: If max decreases, it might indicate peer
   evicted transaction or messages are arriving out of order.  

## References

Implementation will be referenced once it's available.
