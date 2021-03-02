# LAZY ADR 001: Erasure Coding Block Propagation

## Changelog

- 16-2-2021: Created

## Context

Block propagation is currently done by splitting the block into arbitrary chunks and gossiping them to validators via a gossip routine. While this does not have downside it does not meet the needs of the lazy-ledger chain. The lazyledger chain requires blocks to be encoded in a different way and for the proposer to not propagate the chunks to peers. 

Lazyledger wants validators to pull the block from a IPFS network. What does this mean? As I touched on earlier the proposer pushes the block to the network, this in turn means that each validator downloads and reconstructs the block each time to verify it. Instead Lazyledger will encode and split up the block via erasure codes, upload it to IPFS and get back content identifiers. After the proposer has sent the block to IPFS and received the CIDs it will include them into the proposal. This proposal will be gossiped to other validators, once a validator receives the proposal it will begin requesting the CIDs included in the proposal. 

There are two forms of a validator, one that downloads the block and one that samples it. What does sampling mean? Sampling is the act of checking that a portion or entire block is available for download. 

## Detailed Design

The proposed design is as follows.

### Proposal

The current proposal type will from having a blockID to having the available data header containing raw hashes that can be transformed into 

```proto
message Proposal {
  uint64 height = 1;
  uint64 round = 2;
  uint64 timestamp = 3;
  // 32-byte hash
  bytes last_header_hash = 4;
  // 32-byte hash
  bytes last_commit_hash = 5;
  // 32-byte hash
  bytes consensus_root = 6;
  FeeHeader fee_header = 7;
  // 32-byte hash
  bytes state_commitment = 8;
  uint64 available_data_original_shares_used = 9;
  AvailableDataHeader available_data_header = 10;
  // 64-byte signature
  bytes proposer_signature = 11;
}
```


### Disk Storage

Currently Lazyledger-core stores all blocks in its store. Going forward only the headers of the blocks within the unbonding period will be stored. This will drastically reduce the amount of storage required by a lazyledger-core node. After the unbonding period all headers will have the option of being pruned. 

Proposed amendment to `BlockStore` interface

```go 
type BlockStore interface {
	Base() int64
	Height() int64
	Size() int64

	LoadBlockMeta(height int64) *types.BlockMeta
	LoadHeader(height int64) *types.Header

	SaveHeader(header *types.Header, seenCommit *types.Commit)

	PruneHeaders(height int64) (uint64, error)

	LoadBlockCommit(height int64) *types.Commit
	LoadSeenCommit(height int64) *types.Commit
}
```

Along side these changes the rpc layer will need to change. Instead of querying the LL-core store, the node will redirect the query through IPFS. 

Ideally we would not need to change the client facing interface, documentation on this interface is located [here](../../rpc/openapi/openapi.yaml). This means that CIDS will need to be set and loaded from the store in order to get all the related block information an user requires. 

## Status

> A decision may be "proposed" if it hasn't been agreed upon yet, or "accepted" once it is agreed upon. Once the ADR has been implemented mark the ADR as "implemented". If a later ADR changes or reverses a decision, it may be marked as "deprecated" or "superseded" with a reference to its replacement.

{Deprecated|Proposed|Accepted|Declined}

## Consequences

> This section describes the consequences, after applying the decision. All consequences should be summarized here, not just the "positive" ones.

### Positive

### Negative

### Neutral

## References

> Are there any relevant PR comments, issues that led up to this, or articles referenced for why we made the given design choice? If so link them here!

- {reference link}
