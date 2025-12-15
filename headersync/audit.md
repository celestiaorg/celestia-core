# HeaderSync Light Client Sync Audit (2025-12-12)

## Scope
- Review of `headersync` reactor (header streaming + verification) against Tendermint/CometBFT light client expectations.
- Code inspected: `headersync/reactor.go`, `headersync/pool.go`, `headersync/msgs.go` (current workspace state).

## Expected Tendermint Light Client Properties
- Starts from explicit trust options (trusted height/hash) and enforces a trusting period/clock drift window.
- Uses a primary provider plus witnesses to cross-check headers and detect forks; faulty primaries are reported.
- Supports skipping/bisection to jump over long gaps when validator set overlap permits.
- On detected misbehavior, submits evidence for accountability.

## Findings
- **No trusting-period or time checks**: `verifyHeader` only validates signatures/chain linkage; it never rejects headers older than a trusting period or too far in the future. Long-range or equivocation that becomes unpunishable after unbonding could be accepted if a stale trusted header remains in the store. Consider gating acceptance on `header.Time` vs. local clock and a configured trusting period.
- **Single-source verification, no witnesses/fork detection**: The reactor consumes batches from whichever peer answered, without secondary confirmation or conflict detection. A malicious but well-peered primary that controls ≥2/3 of a *stale* validator set could feed a divergent chain indefinitely with no evidence generation. Align with Tendermint’s primary+witness model and add fork detection/reporting.
- **Validator set availability is assumed, not fetched**: `verifyHeader` loads validators from the local `stateStore` for every height but never requests validator sets from peers. After the local state height, header sync will fail (liveness) or, worse, rely on un-updated validator data if state was snapshotted long ago. Tendermint light clients accompany skipped headers with validator sets to keep validation aligned with chain changes.
- **Sequential-only catchup**: The pool requests contiguous ranges starting at `blockStore.HeaderHeight()+1` and cannot use skipping/bisection. Nodes that were offline for long periods must download every intermediate header, increasing exposure to DoS and delaying time-to-safe-state compared with the canonical skipping strategy.
- **No evidence emission on verification failure**: When a header fails verification, the peer is disconnected but no light-client evidence is constructed or forwarded, missing the accountability step expected by Tendermint light clients.

## Recommendations
- **Integrate with existing Evidence Reactor**: On commit verification failure or fork detection, construct `LightClientAttackEvidence` and hand it to the evidence reactor so it can be gossiped/processed. This closes the accountability gap without reinventing plumbing.
- **Inject chain trust parameters**: Thread state-machine settings (e.g., unbonding/trusting period, max clock drift) into `verifyHeader` so headers are rejected when outside the trusting window or too far in the future.
- Add configurable trust options (trusted height/hash, trusting period, max clock drift) and reject headers outside that window.
- Introduce witness peers and fork detection (compare primary responses with witnesses; on conflict, verify both and emit evidence).
- Extend protocol to fetch validator sets alongside headers (or proofs) so validation can progress without pre-populated state.
- Implement skipping/bisection path for large gaps; fall back to sequential only when overlap is insufficient.
- On detected invalid commits, build and submit light client evidence to the consensus layer for accountability.

---

## Validator Set Gossip Design

### Background

Currently headersync assumes validator sets are available locally via `stateStore.LoadValidators(height)`. This works when:
1. The node has executed all blocks up to the requested height
2. State sync bootstrapped the node with recent validator sets

However, headersync peers may have headers without corresponding validator sets (e.g., they also synced via headersync or state synced from a different point). The protocol needs to handle validator set availability gracefully.

### Key Insights

1. **1/3 trust threshold enables skipping**: Using `VerifyCommitLightTrusting` with the default 1/3 trust level, we can verify a header if 1/3+ of a *known* validator set signed it. This means we don't need every validator set—just enough overlap.

2. **Validator sets change infrequently**: On most chains, the validator set changes at most once per epoch (often 1 day+). Headers at heights between validator set changes share the same `ValidatorsHash`.

3. **Headers contain validator hashes**: Each header has `ValidatorsHash` (current) and `NextValidatorsHash` (next block). This creates a cryptographic chain of validator set commitments.

4. **Storage is sparse**: The state store only persists full validator sets at checkpoints (every 100,000 blocks) or when they change. For intermediate heights, only a pointer to `LastHeightChanged` is stored.

5. **State sync provides initial validator sets**: State sync fetches 3 light blocks (at H, H+1, H+2) which include full validator sets. These bootstrap the state store for headersync to continue from.

### Design Goals

1. **Minimal protocol changes**: Extend existing messages rather than adding new ones
2. **Bandwidth efficiency**: Only send validator sets when needed (detected by hash mismatch)
3. **Incremental verification**: Use trust-based skipping when possible, request validator sets only when overlap is insufficient
4. **Peer flexibility**: Handle peers that have headers but not validator sets for all heights

### Protocol Extension

#### Option A: Extend `HeadersResponse` (Recommended)

Add an optional `validator_sets` field to `HeadersResponse`:

```protobuf
message HeadersResponse {
  int64 start_height = 1;
  repeated SignedHeader headers = 2;
  // Optional: validator sets for heights where they changed.
  // Only included when requested or when sender detects receiver may need them.
  // Key is the height, value is the validator set that was active at that height.
  repeated ValidatorSetAtHeight validator_sets = 3;
}

message ValidatorSetAtHeight {
  int64 height = 1;
  tendermint.types.ValidatorSet validator_set = 2;
}
```

This allows:
- Backward compatibility (old nodes ignore the new field)
- Sender can proactively include validator sets at change boundaries
- Minimal overhead when validator set is unchanged across the batch

#### Option B: Request Flag in `GetHeaders`

Add a flag to request validator sets:

```protobuf
message GetHeaders {
  int64 start_height = 1;
  int64 count = 2;
  bool include_validator_sets = 3;  // Request validator sets at change boundaries
}
```

The receiver responds with validator sets included in `HeadersResponse` for heights where `ValidatorsHash` differs from the previous header.

### Verification Flow

#### Current Flow (Sequential, Local Validators)
```
for each header:
  vals = stateStore.LoadValidators(header.Height)  // FAILS if not available
  vals.VerifyCommitLight(commit)                   // Requires 2/3 of vals
  verify header.ValidatorsHash == vals.Hash()
```

#### New Flow (Trust-Based, Gossip-Enabled)

```
knownValSets = map[hash]ValidatorSet  // Cache by hash, seeded from stateStore

for each header in batch:
  // 0. Chain linkage: prevHeader.NextValidatorsHash must equal header.ValidatorsHash
  verifyChainLinkage(header, prevHeader)
  if prevHeader != nil && prevHeader.NextValidatorsHash != header.ValidatorsHash:
    return INVALID_VALIDATOR_TRANSITION

  // 1. Try to find the exact validator set committed to by this header
  vals = knownValSets[header.ValidatorsHash]

  if vals != nil:
    // Full verification against the committed set (2/3 threshold)
    vals.VerifyCommitLight(commit)
  else:
    // Tentative trust-based verification using any known set with >=1/3 overlap
    verified = false
    for knownVals in knownValSets:
      if knownVals.VerifyCommitLightTrusting(commit, 1/3) == nil:
        verified = true
        break

    if !verified:
      requestValidatorSet(header.Height)
      return NEED_VALIDATOR_SET  // header stays untrusted until exact set arrives

  // 2. If validator set was included in response, validate and persist it
  if response.validator_sets[header.Height] != nil:
    newVals = response.validator_sets[header.Height]
    if newVals.Hash() != header.ValidatorsHash:
      return INVALID_VALIDATOR_SET
    if prevHeader != nil && prevHeader.NextValidatorsHash != header.ValidatorsHash:
      return INVALID_VALIDATOR_TRANSITION
    knownValSets[header.ValidatorsHash] = newVals
    persistValidatorSet(height=header.Height,
                        lastHeightChanged=header.Height, // or the real change height if provided
                        valSet=newVals)

  // 3. If we only had trusting verification so far, require a follow-up full check
  if vals == nil && response.validator_sets[header.Height] == nil:
    markPendingFullCheck(header)
```

### Handling Peers Without Validator Sets

A peer may have headers but not validator sets (e.g., they synced via headersync themselves). The protocol should handle this:

1. **Peer responds with headers but empty `validator_sets`**: The requesting node can:
   - Try trust-based verification with existing known validator sets
   - Request from a different peer
   - Fall back to state sync if no peer can provide the validator set

2. **Peer has partial validator sets**: Include what they have. The requesting node merges from multiple sources.

3. **StatusResponse extension** (optional): Add a `has_full_state` flag so nodes can prefer peers with complete validator set history:
   ```protobuf
   message StatusResponse {
     int64 base = 1;
     int64 height = 2;
     bool has_full_state = 3;  // True if peer can provide validator sets for all heights
   }
   ```

### Storage Considerations

When receiving validator sets via gossip:

1. **Cache in memory by hash**: Since validator sets are often reused across many heights, cache by `ValidatorsHash` rather than height.

2. **Persist per-height metadata**: The current `state.Store` expects a `ValidatorsInfo` record at **every** height, with `LastHeightChanged` pointing to the last change and full sets stored at change heights or checkpoints. When ingesting gossiped sets, write `ValidatorsInfo(height, lastHeightChanged, valSet)` so `LoadValidators(h)` and proposer-priority reconstruction continue to work after restart.

3. **Sparse full-set storage, dense pointers**: Persist the full validator set only at change heights (and checkpoint heights per `valSetCheckpointInterval`), but still write pointer entries for intermediate heights to avoid `ErrNoValSetForHeight`.

### State Sync Interaction

State sync seeds the store with validator sets for its fetched light blocks (typically H, H+1, H+2), but it does **not** guarantee coverage for the very next validator change. Header sync must be ready to request/gossip the first post-snapshot change immediately; otherwise verification will stall or fall back to trusting with insufficient overlap.

### Implementation Steps

1. **Extend protobuf messages**: Add `validator_sets` field to `HeadersResponse`

2. **Update sender logic** (`handleGetHeaders`):
   - Include validator sets at heights where `ValidatorsHash` changed
   - Gracefully handle missing validator sets (include what's available)

3. **Update receiver logic** (`processHeaderBatch`):
   - Implement trust-based verification fallback
   - Cache received validator sets by hash
   - Persist new validator sets to state store

4. **Add validator set cache**: In-memory cache keyed by validator set hash for efficient reuse

5. **Update pool logic**: Track which peers can provide validator sets; prefer those peers when verification requires new validator sets

### Security Considerations

1. **Validator set authenticity**: A received validator set must hash to the `ValidatorsHash` in a verified header. Without this check, an attacker could provide a fake validator set.

2. **Trust bootstrapping**: The initial trusted validator set must come from a trusted source (state sync, genesis, or config). All subsequent validator sets are verified through the hash chain.

3. **DoS via large validator sets**: Limit the size of accepted validator sets (already bounded by max validators in consensus params).

4. **Bandwidth amplification**: Only request/send validator sets when needed (hash mismatch). Don't proactively send validator sets for every header.

---

## Dual-Peer Request Strategy

### Motivation

Headers are small (~1KB each, plus commit signatures), making bandwidth overhead minimal. By requesting the same header range from two peers simultaneously, we gain:

1. **Reduced latency**: The first response is used immediately; no waiting for timeouts
2. **Built-in witness verification**: Two independent sources provide natural fork detection
3. **Faster failover**: If one peer is slow or unresponsive, the other continues without delay
4. **DoS resistance**: Harder for a single malicious peer to stall sync

### Design

#### Request Dispatch

When the pool needs headers for range `[start, start+count)`:

1. **Select two peers**: Choose two peers with `status.Height >= start + count - 1`
   - Prefer peers with different IP prefixes (avoid same operator)
   - Prefer peers with `has_full_state = true` if validator sets may be needed
   - If only one peer available, fall back to single-peer mode

2. **Send identical requests**: Dispatch `GetHeaders{start, count}` to both peers simultaneously

3. **Track pending requests**: Store `{peer1, peer2, start, count, sent_at}` in a pending map keyed by `start`

#### Response Handling

When a `HeadersResponse` arrives:

```
response = receive HeadersResponse{start_height, headers}
pending = pendingRequests[start_height]

if pending.firstResponse == nil:
  // First response - use it immediately
  pending.firstResponse = response
  pending.firstPeer = sender

  // Verify and apply headers
  if verify(response.headers) == OK:
    apply(response.headers)
    pending.verified = true
  else:
    // Verification failed - wait for second peer
    // (or disconnect first peer if obviously invalid)
    disconnect(sender)

else:
  // Second response - compare with first
  pending.secondResponse = response
  pending.secondPeer = sender

  if pending.verified:
    // First was already applied - just cross-check
    if headersMatch(pending.firstResponse, response):
      // Consistent - good
      delete pendingRequests[start_height]
    else:
      // FORK DETECTED - both peers sent different valid headers
      handleForkDetection(pending.firstPeer, pending.secondPeer,
                          pending.firstResponse, response)
  else:
    // First failed verification - try second
    if verify(response.headers) == OK:
      apply(response.headers)
      disconnect(pending.firstPeer)  // First peer was faulty
    else:
      // Both failed - disconnect both, try different peers
      disconnect(pending.firstPeer)
      disconnect(sender)
```

#### Timeout Handling

```
checkTimeouts():
  now = currentTime()
  for start, pending in pendingRequests:
    elapsed = now - pending.sent_at

    if elapsed > RESPONSE_TIMEOUT:  // e.g., 10 seconds
      if pending.firstResponse != nil && pending.verified:
        // First already succeeded - second just timed out
        // Mark second peer as slow, continue
        markPeerSlow(pending.secondPeer)
        delete pendingRequests[start]
      else:
        // Neither responded in time - retry with different peers
        disconnect(pending.firstPeer)
        disconnect(pending.secondPeer)
        retryWithNewPeers(start, pending.count)
```

#### Fork Detection

When two peers provide different headers for the same height:

```
handleForkDetection(peer1, peer2, response1, response2):
  // Find first diverging header
  for i in range(min(len(response1.headers), len(response2.headers))):
    h1 = response1.headers[i]
    h2 = response2.headers[i]

    if h1.Hash() != h2.Hash():
      // Both claim to be at same height with different content
      height = response1.start_height + i

      // Verify both commits independently
      valid1 = verifyCommit(h1)
      valid2 = verifyCommit(h2)

      if valid1 && valid2:
        // True fork - both have valid 2/3+ signatures
        // This is a serious protocol violation (equivocation)
        evidence = buildLightClientAttackEvidence(h1, h2)
        submitEvidence(evidence)
        // Disconnect both until evidence is processed
        disconnect(peer1)
        disconnect(peer2)

      else if valid1:
        // Only first is valid - second peer is lying
        disconnect(peer2)

      else if valid2:
        // Only second is valid - first peer is lying
        disconnect(peer1)

      else:
        // Neither valid - both are faulty
        disconnect(peer1)
        disconnect(peer2)

      return
```

### Pool Integration

The pool manages the dual-peer request logic:

```go
type pendingRequest struct {
    peer1, peer2    p2p.ID
    startHeight     int64
    count           int64
    sentAt          time.Time
    firstResponse   *HeadersResponse
    firstPeer       p2p.ID
    verified        bool
}

func (p *Pool) requestHeaders(start, count int64) {
    peers := p.selectTwoPeers(start + count - 1)
    if len(peers) < 2 {
        // Fall back to single peer
        peers = peers[:1]
    }

    pending := &pendingRequest{
        peer1:       peers[0],
        startHeight: start,
        count:       count,
        sentAt:      time.Now(),
    }
    if len(peers) > 1 {
        pending.peer2 = peers[1]
    }

    p.pending[start] = pending

    for _, peer := range peers {
        p.sendGetHeaders(peer, start, count)
    }
}
```

### Bandwidth Analysis

With headers ~1KB and commits ~50KB (500 validators × 100 bytes), each batch of 50 headers is ~2.5MB.

**Single-peer mode**: 2.5MB per batch
**Dual-peer mode**: 5MB per batch (2× bandwidth)

However:
- Latency improvement often 50%+ (use faster peer)
- Fork detection is "free" (built into normal operation)
- Failed responses detected immediately (no timeout wait)
- Headers are still tiny compared to blocks (which include tx data)

The 2× bandwidth cost is acceptable given headers are already lightweight and the security/latency benefits are significant.

### Configuration

```go
type Config struct {
    // DualPeerRequests enables requesting from two peers simultaneously
    DualPeerRequests bool  // default: true

    // ResponseTimeout is how long to wait for header responses
    ResponseTimeout time.Duration  // default: 10s

    // PreferDiversePeers tries to select peers from different IP ranges
    PreferDiversePeers bool  // default: true
}
```

### Interaction with Validator Set Gossip

When using dual-peer requests with the validator set gossip extension:

1. **Validator set inclusion**: Both peers may include different subsets of validator sets
   - Merge validator sets from both responses (after verifying hashes match header)
   - This increases coverage when peers have partial validator set history

2. **Verification order**: Verify headers from whichever response arrives first
   - If verification needs a validator set and first peer doesn't have it, check second response
   - If neither has required validator set, fall back to single-peer request with `include_validator_sets=true`

3. **Consistency check**: Validator sets at same height must match
   - If peer1 includes `ValidatorSet{height=100}` and peer2 includes different set for same height, treat as fork
