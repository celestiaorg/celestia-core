# Header Sync Specification

## Abstract

The Header Sync reactor enables nodes to synchronize block headers ahead of block data download. By decoupling header synchronization from block synchronization, nodes can verify the chain of headers first, then download block data in parallel with knowledge of the expected block size and commitment. This enables the propagation reactor to efficiently catch up by downloading multiple blocks concurrently, knowing in advance which blocks are valid targets.

## Terminology

- **Header**: The block header containing metadata about a block (height, time, proposer, validators hash, data hash, etc.)
- **Commit**: The +2/3 precommit signatures from validators that finalize a block
- **BlockMeta**: A structure containing the BlockID (hash + PartSetHeader), Header, BlockSize, and NumTxs
- **PartSetHeader**: Contains the total number of parts and the Merkle root of the parts
- **Header Height**: The height tracked by the header sync reactor (may be ahead of block height)

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

**R3.1** A node MUST verify that the commit has +2/3 voting power from the validator set at the header's height.

*Rationale: This is the fundamental security property of Tendermint consensus.*

**R3.2** A node MUST verify that the commit's BlockID matches the header hash.

*Rationale: Ensures the commit is for the specific header being validated.*

**R3.3** A node MUST verify that the header's `ValidatorsHash` matches the expected validator set for that height.

*Rationale: Ensures continuity of the validator set chain.*

**R3.4** A node MUST verify that the header's `NextValidatorsHash` matches the validators for height+1.

*Rationale: Enables verification of the next header.*

**R3.5** A node MUST verify that the header's `LastBlockID` matches the previous header's BlockID.

*Rationale: Ensures chain continuity.*

**R3.6** A node SHOULD use light client verification (VerifyCommitLight) for performance during catch-up.

*Rationale: Full signature verification of all validators is expensive; light verification provides sufficient security guarantees.*

**R3.7** A node MUST reject and disconnect from peers that send invalid headers or commits.

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
- Headers: array of (Header, Commit) pairs, sequential starting from StartHeight
- An empty array indicates the peer has no headers from the requested StartHeight

The StartHeight field allows the receiver to match responses to requests without scanning all pending requests. This also enables multiple concurrent requests to the same peer.

## State Machine

```
                    ┌─────────────────────────────────────────┐
                    │                                         │
                    ▼                                         │
              ┌──────────┐                                    │
              │   Idle   │◄────────────────────────────┐      │
              └────┬─────┘                             │      │
                   │                                   │      │
                   │ Peer reports higher header height │      │
                   ▼                                   │      │
              ┌──────────┐                             │      │
              │ Syncing  │────────────────────────────►│      │
              └────┬─────┘  Caught up / error          │      │
                   │                                   │      │
                   │ All requested headers received    │      │
                   │ and verified                      │      │
                   ▼                                   │      │
              ┌──────────┐                             │      │
              │Verifying │────────────────────────────►│      │
              └────┬─────┘  Verification failed        │      │
                   │                                          │
                   │ Verification passed, stored              │
                   │ Notify subscribers                       │
                   └──────────────────────────────────────────┘
```

## Streaming Header Processing

Header sync uses a streaming architecture for improved performance:

1. **Header-by-header verification**: Headers are verified and stored individually as they arrive in order, rather than waiting for an entire batch to complete. This reduces latency to first header.

2. **Pipelined requests**: New batch requests can be issued while previous batches are still being processed, as long as the maximum pending batch limit is not exceeded.

3. **Fine-grained error recovery**: If a header fails verification, only that batch is re-requested from a different peer. Headers already verified from prior batches are preserved.

This streaming approach improves sync throughput by overlapping network I/O with verification and storage.

## Security Considerations

1. **Long-range attacks**: Header sync relies on knowing the correct validator set. Nodes MUST be bootstrapped with a trusted state or use state sync to establish initial trust.

2. **Eclipse attacks**: Syncing from multiple diverse peers reduces the risk of being fed a false chain by a set of colluding malicious peers. Additionally, peers that time out are marked and skipped for subsequent requests, preventing a malicious peer from capturing all requests by claiming the highest height but not responding.

3. **Resource exhaustion**: Request limits, rate limiting, and peer banning prevent attackers from overwhelming nodes with invalid data or excessive requests.

4. **Commit verification**: Light verification (+2/3 voting power) provides the same security guarantees as full verification for honest validators.
