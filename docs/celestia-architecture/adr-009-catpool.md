# ADR 009: Content addressable transaction pool

## Changelog

- 2023-01-11: Initial Draft (@cmwaters)

## Context

One of the criterias of success for Celestia as a reliable data availability layer is the ability to handle large transactional throughput. A component that plays a significant role in this is the mempool. It's purpose is to receive transactions from clients and broadcast them to all other nodes, eventually reaching the next block proposer who includes it in their block. Given Celestia's aggregator-like role whereby larger transactions, i.e. blobs, are expected to dominate network traffic, a content-addressable algorithm, common in many other [peer-to-peer file sharing protocols](https://en.wikipedia.org/wiki/InterPlanetary_File_System), could be far more beneficial than the current transaction-flooding protocol that Tendermint currently uses.

This ADR describes the content addressable transaction protocol and through a comparative analysis with the existing gossip protocol, presents the case for it's adoption in Celestia.

## Decision

Use a content addressable transaction pool for disseminating transaction to nodes within the Celestia Network

## Detailed Design

The core idea is that each transaction can be referenced by a key, generated through a cryptographic hash function that reflects the content of the transaction. Nodes signal to one another which transactions they have via this key, and can request transactions they are missing through the key. This reduces the amount of duplicated transmission compared to a system which blindly sends received transactions to all other connected peers (as we will see in the consequences section).

The new messages: `SeenTx` and `WantTx`, are broadcast over a new mempool channel `byte(0x31)` for backwards compatibility and to distinguish priorities. Nodes running the other mempools will not receive these messages. Transaction gossiping takes priority over these "state" messages as to avoid situations where we receive a `SeenTx` and respond with a `WantTx` while the transaction is still queued in the nodes p2p buffer.




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
> - Does this change require coordination with the Celestia fork of the SDK or celestia-app?

## Alternative Approaches

> This section contains information around alternative options that are considered before making a decision. It should contain a explanation on why the alternative approach(es) were not chosen.

## Status

Proposed

## Consequences

> This section describes the consequences, after applying the decision. All consequences should be summarized here, not just the "positive" ones.

### Positive

### Negative

### Neutral

## References

> Are there any relevant PR comments, issues that led up to this, or articles referenced for why we made the given design choice? If so link them here!

- {reference link}
