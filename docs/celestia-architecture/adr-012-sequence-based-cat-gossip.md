# ADR 012: Sequence-Based Gossip in CAT

## Changelog

- 2026-01-21: proposed

## Context

The previous `pendingSeenTracker` cache could reach capacity and start dropping the
latest arrivals, creating sequence gaps inside the cache. Those gaps made it
hard for peers to request transactions reliably resulting in slowdowns and stalls.

I propose that CAT gossip moves away from txKey-based have signaling to using a
(sequence, signer) model, since nodes already track that information locally.
This lets peers maintain a compact view of what each peer knows without
having to track individual SeenTxs. Nodes gossip their observed sequence ranges per signer,
and we can optimistically request missing transactions based on those advertised ranges,
even if we have not seen the transactions yet.

## Status

Proposed

## Alternative Approaches

Keep the hash-first design and add sequence/signer as a secondary path. Not
chosen because it leaves sequence-first logic scattered across components and
keeps the old mental model as the default.

## Decision

Make the sequence+signer model the primary path across the gossip stack. All new
logic and interfaces should work with `(signer, sequence)` as the tx identifier.

## Detailed Design

### Protocol Components

Proposing to add these components to the mempool gossip stack, while deprecating
the `pendingSeenTracker` and replacing the `txKey` gossip path with `(signer, sequence)` driven gossip.

**Sequence Tracker** (sequence range based):

- Stores only per-peer signer, sequence ranges `[min, max]`.
- Updates ranges when `SeenTx` with min/max is received.
- Cleanup: Remove entries when peers disconnect. Optionally clean up stale signer
  entries (exact mechanism TBD).
- Optimistically requests missing sequences in range, randomly choosing peers while remaining within capacity.

**Request Scheduler**:

The scheduler indexes sequence requests by `string(signer+sequence)`, with the peer ID as the value.
This is used to:

- Prevent duplicate outstanding requests for the same (signer, sequence).
- Enforce per‑peer request limits to avoid overwhelming a peer.
- Only accept txs that match an outstanding request from that peer.
- Retry by requesting the same (signer, sequence) from another peer on timeout,
  while still recognizing late replies briefly.
- Clear all pending sequence requests for a peer on disconnect.

**Upgraded-Only Channel**:

- New channel ID dedicated to upgraded peers only.
- Legacy peers continue to use the existing mempool channels.

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

// Request scheduler request tracking
type requestScheduler struct {
  mu sync.Mutex
  // Primary: sequence-based requests (upgraded peers)
  bySignerSequence map[string]uint16  // string(signer+sequence) -> peerID
}

```

## Messages

The new protocol replaces old gossip messages with the new:

```protobuf
message TxV2 {
  bytes tx        = 1;
  byte signer     = 2;
  uint64 sequence = 3;
}

message SeenTxV2 {
  uint64  sequence     = 1;
  bytes signer         = 2;
  uint64  min_sequence = 3;
  uint64 max_sequence  = 4;
}

message WantTxV2 {
  bytes tx_key    = 1;
  bytes signer    = 2;
  uint64 sequence = 3;
}
```

### Inbound Flow

At a high level, the reactor receives gossip (SeenTxV2), requests (WantTxV2), and responses (TxV2).

1. `SeenTxV2` received
      - Process if possible.
      - Update the (peer, signer) range using min_sequence / max_sequence.
      - If the range suggests we’re missing a needed sequence, send a request.
2. `TxV2` received
      - Verify it matches an outstanding request.
      - Check ordering/sequence expectations based on received sequence:
          - If it matches expected apply `CheckTx`.
          - Otherwise buffer it when its within the lookahead.
3. `WantTxV2` received
      - Check if we still have the requested `(signer, sequence)` tx.
      - If yes, populate send `Tx` message; if not, ignore.

### Outbound Flow

At a high level, the reactor sends gossip (SeenTxV2), requests (WantTxV2), and responses (TxV2).

1. `SeenTxV2` sent when a new tx is added
      - Sent to a bounded set of sticky peers.
      - Include signer, sequence, min_sequence and max_sequence if used.
1. `WantTxV2` sent
      - Sent to a peer that sent us a seen that matches expected sequence.
      - Sent to a peer that advertised the sequence range.
1. `Tx` sent
      - Sent only in response to WantTxV2 when we have the tx.
      - Include signer and sequence.

## Backwards Compatibility

- Legacy peers continue to gossip using `txKey`-based `SeenTx` and `WantTx` messages
  on the legacy channel(s).
- Upgraded peers use the upgraded-only channel and `SeenTxV2` / `WantTxV2` / `Tx`.

## Deprecations

Once all nodes have successfully upgraded:

- Remove the `pendingSeenTracker` and all related cache logic.
- Remove txKey-related logic as a whole.

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

- **Tech debt**: There will be a period where we will support two gossip mechs.

### Areas for Improvement

1. **Specify sequence tracker cleanup mechanism**: Add a concrete proposal for stale signer cleanup(e.g., "Remove signer entries that haven't been updated in the last N blocks and have no active requests").

1. **Range update semantics**: Specify exactly how ranges are updated:
   - `max_sequence`: Always update to maximum seen
   - `min_sequence`: Update when peer advertises a new minimum (they've evicted
     older sequences)
   - Handle reductions in `max_sequence`: If max decreases, it might indicate peer
   evicted transaction or messages are arriving out of order.  

## References

Implementation will be referenced once it's available.
