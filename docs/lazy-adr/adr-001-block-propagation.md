# LAZY ADR 001: Erasure Coding Block Propagation

## Changelog

- 16-2-2021: Created

## Context

Block propagation is currently done by splitting the block into arbitrary chunks and gossiping them to validators via a gossip routine. While this does not have downside it does not meet the needs of the lazy-ledger chain. The lazyledger chain requires blocks to be encoded in a different way and for the proposer to not propagate the chunks to peers. 

Lazyledger wants validators to pull the block from a IPFS network. What does this mean? As I touched on earlier the proposer pushes the block to the network, this in turn means that each validator downloads and reconstructs the block each time to verify it. Instead Lazyledger will encode and split up the block via erasure codes, upload it to IPFS and get back content identifiers. After the proposer has sent the block to IPFS and received the CIDs it will include them into the proposal. This proposal will be gossiped to other validators, once a validator receives the proposal it will begin requesting the CIDs included in the proposal. 

There are two forms of a validator, one that downloads the block and one that samples it. What does sampling mean? Sampling is the act of checking that a portion or entire block is available for download. 

## Detailed Design

> This section does not need to be filled in at the start of the ADR, but must be completed prior to the merging of the implementation.
>
> Here are some common questions that get answered as part of the detailed design:
>
> - What are the user requirements?
>
> - What systems will be affected?
>
> - What new data structures are needed, what data structures will be changed?
>
> - What new APIs will be needed, what APIs will be changed?
>
> - What are the efficiency considerations (time/space)?
>
> - What are the expected access patterns (load/throughput)?
>
> - Are there any logging, monitoring or observability needs?
>
> - Are there any security considerations?
>
> - Are there any privacy considerations?
>
> - How will the changes be tested?
>
> - If the change is large, how will the changes be broken up for ease of review?
>
> - Will these changes require a breaking (major) release?
>
> - Does this change require coordination with the SDK or other?

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
