# Header Sync Specification

## Abstract

The Header Sync reactor enables nodes to synchronize block headers ahead of block data download. By decoupling header synchronization from block synchronization, nodes can verify the chain of headers first, then download block data in parallel with knowledge of the expected block size and commitment. This enables the propagation reactor to efficiently catch up by downloading multiple blocks concurrently, knowing in advance which blocks are valid targets.

Header sync uses **skip verification** with a 1/3 trust threshold to efficiently verify headers without requiring validator sets in the state store. This allows syncing across validator set changes by including validator sets in header responses when they change.

## Terminology

- **Header**: The block header containing metadata about a block (height, time, proposer, validators hash, data hash, etc.)
- **Commit**: The +2/3 precommit signatures from validators that finalize a block
- **BlockMeta**: A structure containing the BlockID (hash + PartSetHeader), Header, BlockSize, and NumTxs
- **PartSetHeader**: Contains the total number of parts and the Merkle root of the parts
- **Header Height**: The height tracked by the header sync reactor (may be ahead of block height)
- **Skip Verification**: Verification using a 1/3 trust threshold instead of full +2/3 verification
- **Trusted Validator Set**: The validator set used for skip verification, updated as headers are processed
- **Batch**: A group of sequential headers requested and processed together (up to 50 headers)

## Requirements

### R1: Header Discovery

**R1.1** Nodes MUST exchange header height status upon peer connection.

*Rationale: Nodes need to know which peers have headers they don't, similar to how blocksync discovers peer block heights.*

**R1.2** Nodes MUST broadcast their status to peers when their header height advances.

*Rationale: Push-based status updates save a round trip compared to pull-based polling. Peers learn about new headers immediately when they become available.*

**R1.3** Nodes MUST track the maximum header height reported by any peer.

*Rationale: This determines the target height for header synchronization.*

**R1.4** Nodes MUST disconnect and ban peers that send status updates with the same or lower height than previously reported.

*Rationale: DoS protection. Since peers should only broadcast when height increases, sending redundant updates indicates misbehavior or a faulty peer.*

**R1.5** When connecting to a new peer, nodes SHOULD send a status with height-1 to avoid triggering DoS protection if a status broadcast occurs shortly after.

*Rationale: If a node's height advances right after adding a peer and it broadcasts the new status, the peer might see two updates with close heights. Sending height-1 initially provides a safety margin.*

### R2: Header Request and Response

**R2.1** A node MUST be able to request a batch of headers starting from a given height.

*Rationale: Batch requests reduce round-trip overhead and improve sync throughput.*

**R2.2** A node MUST NOT request more than 50 headers in a single batch.

*Rationale: Bounds message size and prevents resource exhaustion.*

**R2.3** A node MUST respond to header requests with the requested headers and their commits if available.

*Rationale: The commit is required to verify each header's validity.*

**R2.4** A node MUST respond with as many sequential headers as it has, starting from the requested height.

*Rationale: Partial responses allow progress even when peers have incomplete data. An empty response indicates the peer has no headers from the requested start height.*

**R2.5** Nodes SHOULD request headers from multiple peers in parallel, assigning non-overlapping height ranges.

*Rationale: Parallel requests improve throughput and reduce latency.*

**R2.6** Nodes MUST NOT have more than a configured maximum number of outstanding batch requests.

*Rationale: Prevents resource exhaustion and ensures fair bandwidth distribution.*

**R2.7** Nodes MUST rate-limit incoming GetHeaders requests per peer.

*Rationale: Prevents DoS attacks where a malicious peer spams GetHeaders requests to exhaust disk I/O reading old headers. The rate limit applies per peer with a sliding time window.*

### R3: Header Verification

**R3.1** A node MUST use skip verification (1/3 trust threshold) with the trusted validator set to verify headers.

*Rationale: Skip verification allows syncing across validator set changes without requiring validator sets in the state store. Under an honest majority assumption, if 1/3+ of the trusted validators signed a header, it is trustworthy.*

**R3.2** A node MUST verify the commit's BlockID hash matches the header hash.

*Rationale: Ensures the commit is for the specific header being validated.*

**R3.3** A node MUST verify chain linkage by checking that each header's `LastBlockID.Hash` matches the previous header's hash.

*Rationale: Cryptographically binds the chain. If the last header in a batch is verified, chain linkage proves all intermediate headers are valid.*

**R3.4** A node MUST verify validator hash continuity: each header's `ValidatorsHash` must match the previous header's `NextValidatorsHash`.

*Rationale: Ensures the validator set chain is continuous and prevents validator set substitution attacks.*

**R3.5** A node MUST verify batches by skip-verifying only the last header, then verifying chain linkage backwards.

*Rationale: This is more efficient than verifying each header individually. Chain linkage from a verified header provides transitive trust to all previous headers in the batch.*

**R3.6** When skip verification of the last header fails (<1/3 overlap), a node MUST search backwards for an intermediate header with 1/3+ overlap, verify it, update the validator set, and recursively verify the remaining headers.

*Rationale: Handles the rare case where validator sets changed significantly within a batch.*

**R3.7** A node MUST update its trusted validator set when it receives a header with an attached validator set that matches the header's `ValidatorsHash`.

*Rationale: Enables continued verification after validator set changes.*

**R3.8** A node MUST reject validator sets whose hash does not match the header's `ValidatorsHash`.

*Rationale: Prevents malicious peers from injecting fake validator sets.*

**R3.9** A node MUST reject and disconnect from peers that send invalid headers, commits, or validator sets.

*Rationale: Protects against Byzantine peers wasting resources or attempting attacks.*

### R4: Header Storage

**R4.1** Verified headers MUST be stored in the block store as BlockMeta entries.

*Rationale: Reuses existing storage infrastructure; BlockMeta contains the Header.*

**R4.2** Commits MUST be stored alongside their headers.

*Rationale: Other components may need the commit for verification.*

**R4.3** The block store MUST support headers existing without corresponding block parts.

*Rationale: This is the key change enabling header-first sync.*

**R4.4** Nodes MUST track a separate "header height" distinct from "block height".

*Rationale: Header sync runs ahead of block sync; these heights will differ.*

**R4.5** A node MUST NOT update the block store's contiguous block height based on header-only storage.

*Rationale: The block height invariant (contiguous blocks exist) must be preserved.*

### R5: Header Subscription

**R5.1** The header sync reactor MUST provide a channel for other reactors to receive newly verified headers.

*Rationale: The propagation reactor needs to know about new headers to begin block download.*

**R5.2** Subscribers MUST receive headers in order by height.

*Rationale: Sequential processing simplifies subscriber logic.*

**R5.3** The header sync reactor SHOULD buffer headers for slow subscribers.

*Rationale: Prevents blocking the sync process due to slow consumers.*

### R6: Integration Constraints

**R6.1** Code that loads BlockMeta MUST be updated to handle the case where block parts do not exist.

*Rationale: Previously, BlockMeta existence implied block part existence. This is no longer true.*

**R6.2** The `LoadBlock` function MUST return nil if block parts are missing, even if BlockMeta exists.

*Rationale: Callers expect nil when the block is unavailable.*

**R6.3** Nodes MUST be able to provide headers to peers even when they don't have the block data.

*Rationale: Supports header-first propagation in the network.*

**R6.4** The header sync reactor MUST NOT interfere with normal consensus operation.

*Rationale: Only active during catch-up mode.*

### R7: Peer Management

**R7.1** Nodes MUST track per-peer header request statistics.

*Rationale: Enables selection of faster peers and detection of misbehavior.*

**R7.2** Nodes MUST implement timeout-based retry for unresponsive peers. When a request times out, the peer MUST be marked as timed out and subsequent requests MUST be sent to different peers until the timed-out peer responds successfully.

*Rationale: Prevents stalling due to slow or dead peers. Also mitigates eclipse attacks where a malicious peer with the highest height attempts to capture all requests by not responding, forcing the node to try other peers.*

**R7.3** Nodes MUST ban peers that repeatedly send invalid data.

*Rationale: Protects against resource exhaustion attacks.*

### R8: Operational

**R8.1** The header sync reactor MUST expose metrics for monitoring sync progress.

*Rationale: Operators need visibility into sync status.*

**R8.2** The header sync reactor MUST log significant events (peer issues, verification failures).

*Rationale: Aids debugging and operational awareness.*

## Message Types

### StatusResponse

Contains a peer's header range. Sent proactively when:
- A new peer is added (with height-1 for safety)
- The node's header height advances

Fields:
- Base: lowest header height available
- Height: highest header height available

**Important**: Peers MUST NOT send a StatusResponse with the same or lower height as previously reported. Doing so results in disconnection and banning.

Note: Unlike blocksync, there is no StatusRequest message. Header sync uses a purely push-based model where peers broadcast their status when it changes, eliminating the round-trip overhead of request/response polling.

### GetHeaders

Request for a batch of headers:
- StartHeight: the first header height to retrieve
- Count: number of headers requested (max 50)

### HeadersResponse

Response containing:
- StartHeight: echoes the requested start height, enabling O(1) response matching
- Headers: array of SignedHeader, sequential starting from StartHeight
- An empty array indicates the peer has no headers from the requested StartHeight

The StartHeight field allows the receiver to match responses to requests without scanning all pending requests. This also enables multiple concurrent requests to the same peer.

### SignedHeader

Each SignedHeader contains:
- Header: the block header
- Commit: the commit with +2/3 validator signatures
- ValidatorSet (optional): included only when the validator set changed at this height

Validator sets are attached when `prevHeader.NextValidatorsHash != header.ValidatorsHash`. This signals to the receiver that the validator set changed and provides the new validator set needed for verifying subsequent headers.

**Message size estimate** for 50 headers with 100 validators:
- Header: ~700 bytes each → 35 KB total
- Commit: ~7 KB each → 350 KB total
- ValidatorSet: ~10 KB per change (typically 0-1 per batch)
- Total: ~400 KB typical, well under the 4 MB MaxMsgSize limit

## Event-Driven Architecture

Header sync uses an event-driven architecture rather than timer-based polling:

### Processing Flow

```
Peer Status Update ──► tryMakeRequests() ──► Send GetHeaders to peers
                                                    │
                                                    ▼
Headers Arrive ──────► tryProcessHeaders() ──► Verify Batch
                              │                     │
                              │                     ▼
                              │              Skip verify last header (1/3 threshold)
                              │                     │
                              │                     ▼
                              │              Verify chain linkage backwards
                              │                     │
                              │                     ▼
                              │              Update validator set if changed
                              │                     │
                              │                     ▼
                              │              Store headers & Notify Subscribers
                              │                     │
                              ▼                     ▼
                       tryMakeRequests() ◄───── Continue
```

### Key Principles

1. **Batch verification**: Headers are verified as complete batches using skip verification on the last header plus chain linkage verification. This is more efficient than per-header verification.

2. **Skip verification**: Only one signature verification per batch (on the last header) using a 1/3 trust threshold. Chain linkage provides transitive trust to earlier headers.

3. **Event-triggered requests**: New batch requests are sent in response to events (peer status updates, headers received) rather than on a polling interval.

4. **Passive pool**: The pool is a passive data structure that stores state. All logic is driven by the reactor in response to events.

5. **Self-contained validator updates**: Validator sets are included in header responses when they change, allowing the reactor to update its trusted validator set without external coordination.

### Request Triggering Events

Batch requests are made when:
- A peer sends a status update with a higher height
- Headers are received and processed (freeing pending batch slots)
- A periodic timeout check runs (to handle timed-out requests)

### Error Recovery

If batch verification fails:
1. The entire batch is marked for re-request
2. The peer is disconnected
3. The next request will be sent to a different peer

Headers already verified from prior batches are preserved. The trusted validator set is only updated after successful batch verification.

## Security Considerations

1. **Long-range attacks**: Header sync relies on knowing the correct validator set. Nodes MUST be bootstrapped with a trusted state or use state sync to establish initial trust.

2. **Eclipse attacks**: Syncing from multiple diverse peers reduces the risk of being fed a false chain by a set of colluding malicious peers. Additionally, peers that time out are marked and skipped for subsequent requests, preventing a malicious peer from capturing all requests by claiming the highest height but not responding.

3. **Resource exhaustion**: Request limits, rate limiting, and peer banning prevent attackers from overwhelming nodes with invalid data or excessive requests.

4. **Skip verification security**: The 1/3 trust threshold is safe under an honest majority assumption. If at least 1/3 of the trusted validator set signed a header, at least one honest validator vouched for it. Chain linkage then extends this trust to all headers in the batch.

5. **Validator set authenticity**: Validator sets are only accepted if their hash matches the header's `ValidatorsHash`. Since the header hash commits to `ValidatorsHash`, and the commit binds the header hash, validator sets cannot be forged.

6. **Validator set continuity**: The `NextValidatorsHash` → `ValidatorsHash` continuity check prevents validator set substitution attacks where an attacker tries to inject a different validator set at a height.

7. **Trust period (future)**: Trust period enforcement (rejecting headers older than the trusting period) is deferred to Phase 2. This will provide additional protection against long-range attacks by bounding how far back an attacker can rewrite history.

## Verification Algorithm

Given a batch of headers [H+1, H+2, ..., H+N] and a trusted validator set VS at height H:

### Step 1: Skip Verify Last Header

Try `VerifyCommitLightTrusting(VS, H+N.commit, 1/3)`:
- **If successful**: H+N is trusted. Proceed to Step 2.
- **If failed** (<1/3 overlap): Search backwards for an intermediate header with 1/3+ overlap. If found, verify that header, update VS from its attached validator set, and recursively verify the remaining headers. If no header has 1/3+ overlap, reject the batch.

### Step 2: Verify Chain Linkage

Walk backwards from H+N to H+1, verifying:
- `header[i].LastBlockID.Hash == header[i-1].Hash()` (hash chain)
- `header[i].ValidatorsHash == header[i-1].NextValidatorsHash` (validator continuity)

Also verify that `header[H+1].LastBlockID.Hash` matches the last verified header's hash.

This is O(N) hash comparisons, which is very fast compared to signature verification.

### Step 3: Update Validator Set

Find the last header in the batch with an attached `ValidatorSet`. If found:
1. Verify `ValidatorSet.Hash() == header.ValidatorsHash`
2. Update `currentValidators` for verifying the next batch

### Typical Case Performance

- **One signature verification** per batch (on the last header)
- **N hash comparisons** for chain linkage
- **0-1 validator set updates** per batch (only when validators change)

This is significantly more efficient than per-header +2/3 verification, which would require N full signature verifications per batch.
