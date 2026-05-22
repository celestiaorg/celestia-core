# ADR 012: Chunked and Erasure-Coded Mempool Propagation

## Changelog

- 2026-05-22: Initial draft

## Status

Proposed

## Context

Celestia transactions can carry data blobs up to 8 MiB today, with a roadmap to 32 MiB. The CAT mempool propagates a transaction body as a single `Txs` message after a `SeenTx`/`WantTx` handshake:

- `SeenTx{tx_key, sequence, signer}` announces the existence of a tx.
- The first peer that wants the tx replies with `WantTx{tx_key}`.
- The announcing peer sends the entire body in a `Txs{repeated bytes}` message.

For multi-megabyte blobs this design exhibits four pathologies:

1. **Single-source body transfer.** A 32 MiB blob streams over one TCP connection from one peer at a time. On a 10 MB/s uplink that is ~3.2 s of pure upload before the receiver can forward.
2. **Sequential fan-out.** Each hop in the gossip tree pays a full single-stream upload before its own announce can attract requests.
3. **Head-of-line blocking.** A 32 MiB body occupies `MempoolDataChannel` (`0x31`, priority 3, queue 1000) and starves small-tx traffic queued behind it.
4. **No partial-progress recovery.** If 25 MiB has been received and the source disappears, the whole transfer restarts from a new peer.

At block proposal time `CompactBlock.Blobs` carries only `TxMetaData{hash, start, end}` — the proposal contains no blob bytes and assumes peers already hold the body in mempool. Mempool latency therefore directly bounds block-propagation latency for blob-carrying blocks.

The consensus propagation reactor (`consensus/propagation/`) already solves a similar problem for block parts: 64 KiB chunks, Reed-Solomon `(N,N)` parity, per-chunk Merkle proofs, pull-based recovery (`HaveParts`/`WantParts`/`RecoveryPart`). The mempool can reuse the same primitives at the transaction layer to remove the single-source bottleneck.

## Decision

Introduce a chunked, erasure-coded transaction propagation path that supersedes the legacy `SeenTx`/`WantTx`/`Txs` triplet. Every accepted transaction is encoded into 64 KiB chunks plus Reed-Solomon parity at admission time and gossiped through a new `MempoolChunkChannel`. Receivers stream chunks from multiple peers in parallel; any K-of-2K chunks reconstruct the body. A tx enters the reapable mempool only after the full body is reconstructed, re-hashed, and re-validated by `CheckTx`.

### Locked design parameters

| # | Decision | Choice |
|---|---|---|
| 1 | Chunk size | 64 KiB (`types.BlockPartSizeBytes`) |
| 2 | Erasure ratio | K originals → 2K total (Reed-Solomon `(K, K)`), any K reconstruct |
| 3 | P2P channel | New `MempoolChunkChannel = 0x33` |
| 4 | Path architecture | Unified chunked path; legacy `SeenTx`/`WantTx`/`Txs` removed after one release of overlap |
| 5 | Announce redundancy | Graduated fanout: `fanout = max(1, ceil(T / num_parts))`, target `T = 60` |
| 6 | Announce commitment | `SeenLargeTx` carries `parts_root` plus full leaf-hash array (mirrors `CompactBlock.parts_hashes`) |
| 7 | 1-chunk txs | Skip parity entirely; single `TxChunk` with no proof; `parts_root == hash(chunk)` |
| 8 | Backward compatibility | Channel-presence negotiation; legacy paths remain one release before deletion |
| 9 | Per-peer cap | `ceil((num_parts/2) / ceil(connected_peers * 0.33))` per tx (same formula as `consensus/propagation/reactor.go:376`) |
| 10 | Retry interval | 2500 ms (matches `consensus/propagation/reactor.go:42`) |
| 11 | Scheduling | FIFO over `receivedHaves` (matches propagation reactor) |
| 12 | Reapability | Full reconstruct → `SHA256(body) == tx_key` → `CheckTx` OK |
| 13 | Chunk retention | Keep originals + parity until the tx is evicted from mempool |
| 14 | Partial TTL | 30 s from first chunk received |
| 15 | Memory caps | Global 256 MiB plus per-peer 64 MiB for partials |
| 16 | Encode timing | Synchronous on `CheckTx` admission |
| 17 | Sender bootstrap | Push K chunks across ~4 peers, then pure pull |

## Detailed design

### Wire protocol

Additions to `proto/tendermint/mempool/types.proto`:

```protobuf
message SeenLargeTx {
  bytes  tx_key      = 1;   // SHA256(body), application-level identifier
  bytes  parts_root  = 2;   // Merkle root over (originals || parity)
  uint32 num_parts   = 3;   // 2K total, or 1 for single-chunk no-parity
  uint32 last_length = 4;   // byte length of last original chunk (un-padded)
  bytes  signer      = 5;   // existing CAT field; up to 64 bytes
  uint64 sequence    = 6;   // per-signer ordering, mirrors today's SeenTx
  repeated bytes leaf_hashes = 7; // 32 B * num_parts; same role as CompactBlock.parts_hashes
}

message HaveTxChunks {
  bytes                         tx_key = 1;
  tendermint.libs.bits.BitArray parts  = 2 [(gogoproto.nullable) = false];
}

message WantTxChunks {
  bytes                         tx_key = 1;
  tendermint.libs.bits.BitArray parts  = 2 [(gogoproto.nullable) = false];
}

message TxChunk {
  bytes                   tx_key = 1;
  uint32                  index  = 2;
  bytes                   data   = 3;   // 64 KiB; last original may be shorter, parity is full
  tendermint.crypto.Proof proof  = 4 [(gogoproto.nullable) = false]; // omitted for num_parts == 1
}

message Message {
  oneof sum {
    Txs           txs            = 1; // legacy, deprecated
    SeenTx        seen_tx        = 2; // legacy, deprecated
    WantTx        want_tx        = 3; // legacy, deprecated
    SeenLargeTx   seen_large_tx  = 4;
    HaveTxChunks  have_tx_chunks = 5;
    WantTxChunks  want_tx_chunks = 6;
    TxChunk       tx_chunk       = 7;
  }
}
```

### Channel

```
MempoolChunkChannel = byte(0x33)
  Priority             6     // above MempoolDataChannel (3), below propagation (140+)
  SendQueueCapacity    2000
  RecvMessageCapacity  ~600 KiB (chunk + proof + framing)
```

Peer support is detected by presence of channel `0x33` in the peer's reactor channel list. Peers that do not advertise it fall back to the legacy path for the deprecation window.

### Encoder

Encoding mirrors `types.Encode` (`types/part_set.go:312`):

1. Split body into K originals of `BlockPartSizeBytes` (64 KiB). The last original may be shorter.
2. Allocate a parity buffer of size `K * BlockPartSizeBytes`.
3. Pad the last original to `BlockPartSizeBytes` with zeros before invoking `reedsolomon.New(K, K).Encode`.
4. Compute `parts_root` and `leaf_hashes` as `merkle.ParallelProofsFromByteSlices(append(originals, parity...))`.
5. Record `last_length` so the receiver can trim padding after reconstruct.

For `K = 1` the encoder skips Reed-Solomon entirely and `parts_root = hash(chunk)`.

### Store

Per-tx state:

```go
type partsState struct {
    txKey       types.TxKey
    partsRoot   []byte
    numParts    uint32     // 2K, or 1 for the no-parity case
    lastLength  uint32
    leafHashes  [][]byte   // length == numParts
    chunks      [][]byte   // index → bytes; nil until received

    haves       map[uint16]*bits.BitArray  // peer → bitmap of chunks they advertise
    inflight    map[uint16]*bits.BitArray  // peer → bitmap of chunks we have outstanding from them
    received    *bits.BitArray             // chunks we have

    firstChunkAt time.Time
    state        partsLifecycle             // collecting | reconstructed
}
```

A single global lock guards admission/eviction; per-tx state uses its own mutex.

Memory accounting:

- `globalPartialBytes` (atomic) tracks bytes held in `collecting` state across all partials. Default cap 256 MiB (`partial_global_max_bytes`).
- `peerPartialBytes[peerID]` tracks bytes attributable to partials whose `SeenLargeTx` originated from `peerID`. Default cap 64 MiB (`partial_per_peer_max_bytes`).
- On admission overflow: evict the oldest partial of the offending bucket and downscore its originating peer.

The 30 s TTL (`partial_tx_ttl`) runs from `firstChunkAt`. A janitor goroutine sweeps expired partials every 5 s.

### Graduated announce-fanout

When the originator (or any later forwarder) sends `SeenLargeTx`, the recipient set is determined by:

```
target           = 60                          // chunked_announce_target
peers_per_chunk  = max(1, ceil(target / num_parts))
```

`peers_per_chunk` peers are drawn from the existing sticky-peer rendezvous ordering (`reactor.go:581`) so the distribution stays consistent with current per-signer routing. For 1-chunk txs, this yields fanout 60; for a 1024-chunk (32 MiB) tx, fanout collapses to 1 because chunks themselves carry the redundancy.

### Receiver pipeline

On `SeenLargeTx`:
1. Validate sizes (`signer ≤ 64`, `num_parts > 0`, `len(leaf_hashes) == num_parts`).
2. Apply per-signer sequence ordering exactly as `SeenTx` does today (`pending.go`). Out-of-sequence announces go to `pendingSeen`.
3. Create a `partsState` in `collecting`.
4. Schedule initial `WantTxChunks` to the announcer using the per-peer cap formula.

On `HaveTxChunks`:
1. Look up the `partsState`; if absent ignore.
2. Update `haves[peer]`.
3. If we have any missing chunks the peer advertises and we have request capacity to that peer, send a `WantTxChunks` covering up to the per-peer cap.

On `WantTxChunks`:
1. Look up `partsState`. For each chunk index requested that we hold, send a `TxChunk` with a Merkle proof against `partsRoot` (omitted for `num_parts == 1`).
2. If we are still collecting and do not hold a requested chunk, ignore (peer will retry elsewhere).

On `TxChunk`:
1. Verify `proof.Verify(partsRoot, data) == nil` (or `hash(data) == partsRoot` for `num_parts == 1`).
2. Install `chunks[index] = data`; mark `received.SetIndex(index)`.
3. If `received.PopCount() >= K`, attempt decode:
   - Reed-Solomon-reconstruct missing originals.
   - Trim the last original to `lastLength`.
   - Concatenate originals → body.
   - Check `SHA256(body) == tx_key`; reject and ban-score the originating peer on mismatch.
   - Submit body to `CheckTx`; if accepted, promote state to `reconstructed` and admit into the existing CAT pool. `partsState` is retained (with chunks) until pool eviction.

### Scheduling

The per-peer concurrent-request cap reuses the propagation formula:

```
cap_per_peer_per_tx = ceil((num_parts / 2) / ceil(connected_peers * 0.33))
```

Within a single tx, missing chunks are walked in FIFO order on the `receivedHaves` channel from each peer (matches `consensus/propagation/have_wants.go`).

Retry: every 2500 ms a ticker walks `inflight` BitArrays. Wants older than the retry deadline are cancelled and re-issued to a different peer advertising the missing chunk.

### Sender bootstrap

When a transaction enters via `CheckTx`:

1. Synchronously RS-encode (target latency 10–30 ms for K=512).
2. Build `partsState` in `reconstructed` state with all chunks present.
3. Select 4 peers via sticky rendezvous and push, in parallel, a random subset of `{originals ∪ parity}` summing to K total chunks (≈ K/4 chunks each, uniformly random within the 2K-chunk set).
4. Emit `SeenLargeTx` to the graduated-fanout target set.

This guarantees K chunks are scattered across ≥ 4 peers within one RTT of admission, so the network can reconstruct without any one peer being a single point of failure.

### Reapability

`ReapMaxBytesMaxGas` (`mempool/cat/pool.go:441`) is unchanged. Only transactions in `reconstructed` state are inserted into `txStore` / `txSets`. Partial chunked txs are invisible to the proposer.

### Eviction

Partial: TTL of 30 s from first chunk; on expiry the partial is dropped and the announcing peer is recorded for downscoring.

Reconstructed: standard CAT eviction. When `wrappedTx` is removed from `txStore`, the corresponding `partsState` (including chunks) is dropped.

## Configuration

Added to `config.MempoolConfig`:

```toml
[mempool]
# Below this size, txs use the no-parity 1-chunk path (still on chunked channel).
# Above this size, full K-of-2K encoding applies.
# 0 means use the default (BlockPartSizeBytes = 65536).
chunk_size_bytes = 65536

# Target number of SeenLargeTx announcements per tx, distributed across chunks.
chunked_announce_target = 60

# How long a partial chunked tx may sit incomplete before being evicted.
partial_tx_ttl = "30s"

# Global cap on bytes held in collecting partials.
partial_global_max_bytes = 268435456   # 256 MiB

# Per-peer cap on bytes held in collecting partials attributed to that peer.
partial_per_peer_max_bytes = 67108864  # 64 MiB

# Number of peers the originator pushes K chunks to on admission.
bootstrap_push_peers = 4
```

## Phased implementation

1. **Wire + plumbing.** Add proto messages, regen `types.pb.go`, register `MempoolChunkChannel`. Channel-presence negotiation only; no behavior change.
2. **Encoder + store, sender-side.** Implement `mempool/cat/chunked/encoder.go` and `store.go`. On `CheckTx` success, encode into chunks but continue gossiping via legacy `Txs`. Telemetry only (`chunked_encode_duration`, `chunked_partial_bytes`).
3. **Receiver pipeline.** Handle `SeenLargeTx`/`HaveTxChunks`/`WantTxChunks`/`TxChunk`. Scheduler with FIFO + per-peer cap + 2500 ms retry. Behind a feature flag.
4. **Sender bootstrap push.** Push K chunks to 4 peers at admission. Validates the latency win against a fully pull-based baseline.
5. **Graduated fanout.** Switch announces to the `T / num_parts` fanout formula.
6. **Flip default and deprecate.** Chunked path becomes default; legacy `SeenTx`/`WantTx`/`Txs` remain compiled for one release before deletion.

## Risks

- **Sync encode at admission.** Reed-Solomon for K = 512 (32 MiB) is ~10–30 ms on commodity hardware. Must be benchmarked under load; if it stalls the RPC, fall back to background encode (Phase 2 telemetry will guide this).
- **Announce size for large txs.** A 32 MiB tx produces a ~32 KiB `SeenLargeTx`. At fanout = 4 (bootstrap) this is ~128 KiB of announce traffic, manageable but worth measuring.
- **Memory caps under stress.** Global 256 MiB plus per-peer 64 MiB can be exhausted by adversarial announce floods. Eviction and peer downscoring must be exercised under fault-injection testing.
- **Legacy interop window.** Dual-protocol nodes carry both code paths. We must publish a release schedule for legacy deletion so operators do not skip the overlap release.

## Future work

- **Block-proposer awareness of partial chunks.** When `CompactBlock` references a tx for which we hold a partial chunk set, accelerate the remaining-chunk fetch.
- **Rarest-first scheduling** as a Phase-7 swap-in for FIFO.
- **Peer scoring** by partial-tx abandonment rate.
- **Bandwidth-budget per peer** as a refinement of the chunk-count cap.
