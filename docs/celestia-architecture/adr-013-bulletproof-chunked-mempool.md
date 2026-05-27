# ADR 013: Bullet-proof Chunked Mempool Propagation

## Changelog

- 2026-05-27: Initial draft (supersedes [ADR-012](./adr-012-chunked-mempool-propagation.md))
- 2026-05-27: Post-bench revisions — see "Revisions" section. Default RPC announce broadened to all peers; HaveTxChunks gossip batched per TxChunks receipt; control-plane messages (SeenLargeTx, HaveTxChunks, WantTxChunks) use blocking `Send` to prevent silent drops under load.

## Status

Proposed — supersedes ADR-012.

## Context

ADR-012 introduced chunked + erasure-coded propagation for large mempool transactions but its execution path accumulated complexity: speculative pulls, sticky-peer selection, dual-mode RPC handling, TTL eviction races, retries layered on retries. Live experiments on a 89-validator network showed dissemination working in principle but stalling under load, with subtle interactions between the legacy `SeenTx`/`WantTx`/`Txs` admission path and the chunked path masking the real propagation behavior.

This ADR restarts the design with three explicit node roles, a strict pull-only data plane, no timer-based retries, and reliance on K-of-2K erasure-coding redundancy plus broad HaveTxChunks gossip for liveness. The chunk size (64 KiB), erasure ratio (K → 2K Reed-Solomon), per-chunk Merkle proofs against `parts_root`, and channel ID (`MempoolChunkChannel = 0x33`, priority 6, send queue 2000) carry over from ADR-012 unchanged.

## Three node roles (per-tx)

A node's role is determined per transaction, not per node identity:

| Role | Trigger | Behavior on origination |
|---|---|---|
| **Default RPC** | tx admitted via local RPC, env `RPC` unset | SeenLargeTx + HaveTxChunks(partial) bundled to 15 random peers; serve TxChunks on Want |
| **Push RPC** | tx admitted via local RPC, env `RPC=1` set | SeenLargeTx + HaveTxChunks(full bitmap) to **all** peers; push every chunk to 6 round-robin peers; serve TxChunks on Want |
| **Intermediate** | tx not admitted by this node — learned via gossip | Forward SeenLargeTx fanout-15; on chunk receipt announce HaveTxChunks to all peers not known to have it; on reconstruction announce full-bitmap HaveTxChunks to all peers not known to have all chunks |

The same node acts as Intermediate for all gossiped txs and as Default/Push RPC only for the specific txs it admits through its own RPC.

## Wire protocol

`proto/tendermint/mempool/types.proto` — final message set on channel `0x33`:

```proto
// Announce: tx exists, here is its shape and leaf hashes.
message SeenLargeTx {
  bytes          tx_key      = 1;
  bytes          parts_root  = 2;
  uint32         num_parts   = 3; // 2K (data + parity); 1 for K=1 degenerate
  uint32         last_length = 4;
  bytes          signer      = 5;
  uint64         sequence    = 6;
  repeated bytes leaf_hashes = 7; // 32 B per chunk; enables partial-state serving
}

// Announce: sender holds these chunks for this tx.
message HaveTxChunks {
  bytes                         tx_key = 1;
  tendermint.libs.bits.BitArray parts  = 2 [(gogoproto.nullable) = false];
}

// Request: send me these chunks.
message WantTxChunks {
  bytes                         tx_key = 1;
  tendermint.libs.bits.BitArray parts  = 2 [(gogoproto.nullable) = false];
}

// Response: here are up to 16 chunks + proofs.
message TxChunks {
  bytes              tx_key = 1;
  repeated TxChunk   chunks = 2;
}

message TxChunk {
  uint32                  index = 1;
  bytes                   data  = 2;
  tendermint.crypto.Proof proof = 3 [(gogoproto.nullable) = false];
}
```

Legacy `SeenTx`, `WantTx`, `Txs` messages are removed from the wire entirely. Channel `0x33` is the only mempool propagation channel.

## Data plane flows

### Default RPC origination (revised — see "Revisions" below)

1. Tx arrives via RPC. `CheckTx` admits it.
2. Encode body into K data chunks + K Reed-Solomon parity chunks (2K total).
3. Compute Merkle proofs for all 2K chunks → `parts_root`, cache `leaf_hashes` + per-chunk `Proof`.
4. For each chunk `i ∈ [0, 2K)`, assign 2 announce-targets via round-robin across **all** connected peers: peer indices `(i·2) mod P` and `(i·2 + 1) mod P`. Each peer receives a HaveTxChunks bitmap covering ~`⌈2·2K / P⌉` chunks (often only 1–2 for typical P).
5. To **every** connected peer, send (in order): `SeenLargeTx` (full, with leaf_hashes), then `HaveTxChunks{parts = that peer's assigned subset}` (omitted for peers whose bitmap is empty).
6. Origin serves `TxChunks` in response to `WantTxChunks` until tx is committed or evicted.

### Push RPC origination

1. Tx arrives via RPC; env `RPC=1` is set. `CheckTx` admits it.
2. Encode + Merkle setup as above.
3. To **every** connected peer, send: `SeenLargeTx`, then `HaveTxChunks{parts = full 2K bitmap}`.
4. For each chunk `i ∈ [0, 2K)`, push the chunk data to 6 distinct round-robin peers via `TxChunks` (batched up to 16 chunks per message). Across 2K chunks × 6, each peer receives ~`12K / numPeers` chunks proactively.
5. Origin serves `TxChunks` in response to `WantTxChunks` until tx is committed or evicted.

### Intermediate flow

On `SeenLargeTx{txKey, ...}` received from peer P:
- If we already have a `PartsState` for `txKey`: record P as having the announced chunks (none yet from this single message — chunks are conveyed by HaveTxChunks); ignore.
- Else: validate `leaf_hashes` commit to `parts_root` (reject + disconnect P on mismatch). Insert `PartsState`. Forward `SeenLargeTx` to 15 random peers (excluding P and any already in `seenByPeersSet[txKey]`).

On `HaveTxChunks{txKey, parts}` received from peer P:
- For each chunk index `i` set in `parts`:
  - Record `haves[P][i] = true` in the `PartsState`.
  - If we already have chunk `i`: skip.
  - If chunk `i` is already in-flight (per-PartsState `inFlight[i] == true`): append P to `backupAnnouncers[i]`. Do not issue a Want.
  - Else: enqueue chunk `i` for the next Want batch to P.
- Send a `WantTxChunks{txKey, parts = batched-missing}` to P (up to 16 chunks, respecting per-peer in-flight cap of 16). Mark those chunks `inFlight[i] = true`.

On `TxChunks{txKey, chunks}` received from peer P:
- For each `{index, data, proof}` chunk:
  - Verify `proof` against `PartsState.parts_root`. **On failure: disconnect P immediately.**
  - If chunk already held: drop.
  - Install chunk. Clear `inFlight[index]`. Decrement P's in-flight count.
- After all chunks in the batch are installed, broadcast **one** `HaveTxChunks{txKey, parts = newly-installed-bits}` per peer `Q` where `Q` doesn't already know we hold those chunks. One message per peer per batch, not per chunk — keeps gossip traffic O(peers) rather than O(peers × chunks).
- After installation, if we now hold ≥ K of the 2K chunks: reconstruct body via RS decode, re-hash to verify `tx_key`, call `CheckTx`, admit on success.

On `WantTxChunks{txKey, parts}` received from peer P:
- For each chunk index `i` set in `parts` that we hold (data or parity, including those derived after reconstruction): assemble `TxChunks` response (up to 16 per message; multiple messages if needed). Each chunk carries its cached `proof`.
- Chunks we don't hold are silently omitted from the response.

### Post-reconstruction (Intermediate becomes a full source)

After successful reconstruction:
1. Re-encode body to derive any parity chunks we didn't receive. Now hold all 2K chunks + cached proofs.
2. Send `HaveTxChunks{txKey, parts = full 2K bitmap}` to all peers `Q` such that `haves[Q]` is not full (i.e., we don't yet believe Q has all chunks). This is the post-admit "I have everything" broadcast.

Default RPC and Push RPC origins skip this step — they already hold all 2K at admission time.

## Want mechanics

| Property | Value |
|---|---|
| Target peer | First peer that announced HaveTxChunks for the chunk |
| Backup announcers | Queued in arrival order per chunk; consumed only on peer-disconnect events |
| Cross-peer dedup | Per-PartsState `inFlight` bitmap; never issue a parallel Want for an in-flight chunk |
| Per-peer in-flight cap | 16 chunks (`WantTxChunks` bundle size = 16) |
| Response batch cap | 16 chunks per `TxChunks` (fits under default `maxMsgSize`) |
| Retry on timeout | **None.** No timer-based retry. Failures clear only on peer-disconnect or chunk receipt. |
| Liveness guarantee | K-of-2K coding tolerates K unfulfilled Wants; broad HaveTxChunks gossip ensures alternate announcers |

The absence of timer-based retry is intentional: with K-of-2K, losing up to K chunks is recoverable, and the broad HaveTxChunks gossip from every chunk-receiver means each chunk is announced by many peers. If a Want stalls, the chunk simply doesn't arrive from that path; reconstruction proceeds with the other ~K chunks from other paths.

## Failure modes

| Event | Action |
|---|---|
| Peer disconnect | Clear all `inFlight` entries Want-ed from that peer. Promote backup announcer for each affected chunk. Remove peer from `haves` map. |
| Merkle proof failure on TxChunks | Disconnect peer immediately (`Switch.StopPeerForError`). |
| `leaf_hashes` don't commit to `parts_root` in SeenLargeTx | Reject SeenLargeTx; disconnect peer. |
| All known announcers exhausted (no peer holds chunk i) | PartsState stays in store; chunk waits indefinitely for a future HaveTxChunks. K-of-2K typically saves us; if reconstruction never completes, PartsState eventually evicted by global cap LRU. |
| `CheckTx` fails on reconstructed body | Drop PartsState; record `txKey` as invalid (do not re-admit if announced again in TTL window). |

## Lifecycle

- **Encoding timing**: synchronous in `Pool.CheckTx` admission for RPC-admitted txs; lazy on first `SeenLargeTx` insertion for Intermediates.
- **Memory caps**: global 256 MiB across all `PartsState`. LRU eviction on overflow. No TTL — entries live until admitted, committed, or LRU-evicted.
- **Block inclusion**: on `Mempool.Update`, the mempool drops the tx; the chunked store drops the `PartsState` (chunks + proofs freed immediately).
- **No sequence-aware buffering**: chunks for any (signer, sequence) are pulled independently of prior sequences. Out-of-order reconstruction is tolerated; mempool's internal ordering handles admission sequencing.

## Locked parameters

| # | Decision | Choice |
|---|---|---|
| 1 | Chunk size | 64 KiB (`types.BlockPartSizeBytes`) |
| 2 | Erasure ratio | K data + K parity = 2K; any K reconstruct |
| 3 | P2P channel | `MempoolChunkChannel = 0x33`, priority 6, send queue 2000 |
| 4 | Legacy path | `SeenTx`/`WantTx`/`Txs` removed entirely |
| 5 | Threshold | All txs through chunked (K=1 degenerate) |
| 6 | HaveTxChunks role | Pure bitmap announcement (no data) |
| 7 | Origin SeenLargeTx fanout (Default) | **All connected peers** (revised) |
| 8 | Origin chunk announce (Default) | 2× per chunk, spread across all peers (round-robin) (revised) |
| 9 | Origin chunk announce (Push) | All peers receive SeenLargeTx + HaveTxChunks(full) |
| 10 | Origin chunk push (Push only) | 6× per chunk via round-robin TxChunks |
| 11 | SeenLargeTx payload | Includes `leaf_hashes` (enables partial-state serving) |
| 12 | Intermediate SeenLargeTx forward | Random fanout-15, excluding sender + `seenByPeersSet` |
| 13 | Intermediate chunk-receipt Have gossip | All peers not already known to hold the chunk |
| 14 | Want trigger | Only on HaveTxChunks receipt (no speculative Want from SeenLargeTx) |
| 15 | Want target | First announcer; backup queue consumed on peer disconnect |
| 16 | Cross-peer dedup | Per-PartsState `inFlight` bitmap |
| 17 | Want batch size | Up to 16 chunks per `WantTxChunks` |
| 18 | Response batch size | Up to 16 chunks per `TxChunks` |
| 19 | Per-peer in-flight cap | 16 chunks |
| 20 | Want timeout / retry | None |
| 21 | Bad chunk action | Disconnect peer immediately |
| 22 | Post-admit Intermediate | Send full-bitmap HaveTxChunks to peers not known to hold all |
| 23 | PartsState TTL | None |
| 24 | PartsState memory cap | 256 MiB global, LRU evicted |
| 25 | Sequence buffering | None — mempool handles ordering |
| 26 | Block inclusion cleanup | Drop PartsState immediately |
| 27 | Role detection | Per-tx: RPC-admit → Default; + env `RPC=1` → Push; gossip → Intermediate |

## Consequences

### Positive

- **Three clearly-bounded roles** eliminate the per-tx mode confusion of ADR-012.
- **Pure pull data plane** (except Push RPC's initial seed) makes traffic predictable: every byte on `0x33` either announces availability or fulfills a specific request.
- **No retry timers** removes a major source of flakiness; correctness rides on K-of-2K and broad HaveTxChunks gossip.
- **Partial-state serving via `leaf_hashes`**: a node holding 1 of 2K chunks can still serve that 1 with a valid proof, dramatically widening the effective announcer set.
- **Bandwidth profile per origin**: Default RPC ≈ 1× tx size (serves what's pulled); Push RPC ≈ 6× tx size + N small announces. Intermediates ≈ 1× tx size receive + 1× send (gossip + serve).

### Negative

- **Push RPC's 6× upload from origin** is substantial — appropriate only for dedicated relay/sequencer roles, not for normal validators. Gated behind env `RPC=1`.
- **No retry** means a Want lost to a peer-side bug stays lost until that peer disconnects. K-of-2K margin is the only recovery mechanism.
- **No TTL** means a stuck PartsState (chunks announced but never delivered) lingers until LRU pressure removes it — could mask deeper bugs.
- **Wire change**: `TxChunk` → `TxChunks` (plural). One-shot break of network compatibility with ADR-012-era nodes; coordinated upgrade required.

## Revisions (post first deploy)

The initial draft ran on a custom 89-validator network and showed tx-inclusion latencies of 13–27 seconds for ~2 MB blobs — roughly 5–10 block intervals. The diagnosis was a combination of three issues that the original design didn't account for:

1. **HaveTxChunks gossip storm.** Each chunk receipt fired one HaveTxChunks per peer (~99 messages), so a 32-chunk batch produced ~3,000 messages per receiver and ~45 k network-wide. With send queues fixed at 2,000, `TrySend` returned false silently for the tail of every burst; some peers never learned who held some chunks.
2. **Narrow Default RPC announce.** With SeenLargeTx + HaveTxChunks bundled to only 15 peers, the other ~75 % of the network waited on second-hop gossip to discover the tx's shape — and that second hop was exactly what the gossip storm was choking. If the next block proposer was outside the lucky 15, the tx skipped multiple blocks before inclusion.
3. **No retry + silent drops.** "First-announcer-only Want" + "no Want timeout" relies on Want and HaveTxChunks reliably arriving. With `TrySend` dropping messages under burst, the K-of-2K margin was the only liveness mechanism, and clustered drops occasionally exceeded the margin.

The three corresponding fixes, applied together:

### Fix A — batched HaveTxChunks gossip

`handleTxChunks` accumulates the bitmap of chunks installed during one TxChunks batch and emits **one** HaveTxChunks per peer covering those bits, instead of one HaveTxChunks per chunk. Gossip traffic on chunk receipt drops by ~`batchSize` × (typically 16×). Same `PeerKnowsChunk` dedup as before, just collapsed across the batch.

### Fix B — broaden Default RPC announce

Default RPC origination sends SeenLargeTx + HaveTxChunks (partial bitmap) to **all** connected peers, not 15. The 2× chunk-announce redundancy now spreads across the full peer set: chunk `i` → peers `(i·2) mod P` and `(i·2 + 1) mod P`. Each peer immediately learns the tx's shape and at least the chunks origin has assigned to them; nobody waits on second-hop gossip. Origin's upload load stays bounded because each chunk is uploaded only ~2 times in steady state.

### Fix C — blocking `Send` for control plane

SeenLargeTx, HaveTxChunks, and WantTxChunks are sent with blocking `Send` (the p2p layer blocks until the per-peer send queue accepts) instead of `TrySend`. TxChunks data still uses `TrySend` — chunk data is bulk (~64 KiB per chunk), and the K-of-2K erasure margin absorbs a small fraction of data drops. Control-plane reliability is restored without changing the no-retry decision: under load, broadcasts back-pressure rather than vanish.

Net effect: the design now has the property "no retry" originally assumed — that control messages reliably arrive — because the underlying transport stops silently dropping them.

## References

- ADR-012 (superseded): initial chunked + erasure-coded design with TTL/retry/sequence handling.
- `consensus/propagation/reactor.go` — same Reed-Solomon + Merkle primitives applied at the block-parts layer.
- `mempool/cat/chunked/` — store, encoder, leaf-proof packages reused as-is.
