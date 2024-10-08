# ADR 010: RBF mempool

## Changelog

- 2024-08.10.24: Initial draft (@ninabarbakadze)

## Context

A key aspect of user experience for any blockchain system, including Celestia, is the smooth and reliable transaction submission. One of the critical components in achieving this is a mempool that not only handles high tx volume gracefully but also manages transaction ordering effectively to minimize unnecessary rejections. The current mempool implementations priorit and cat prioritize transactions solely based on fees without considering nonce order. Lack of nonce awareness in mempools can result in otherwise valid transactions being rejected with nonce errors due to a higher nonce transaction is prioritized while a lower nonce transaction is still pending.

## Decision

Extend the current mempool implementation to be nonce aware.

## Detailed Design

in order for the mempool to become aware of nonces we'd have to have Transactions partially ordered by both nonce and priority, ensuring high-priority transactions are processed sooner while maintaining nonce order. The internal structure would consist of The internal structure consists of: A priority-ordered skip list for overall transaction sorting
Separate skip lists for each sender, ordered by transaction nonces.

when a new transaction is added to the mempool ABCI CheckTx method is called where a transaction has to satisfy a number of conditions before it's accepted in node's mempool. CheckTx also returns a number of fields like the sender and priority. We'd need to extend ABCI's `ResponseCheckTx` to include the nonce.

`AddTransaction` deals with the `CheckTx` response. Here we could already start indexing transactions by their sender and nonce. Essentially this would be a map on `TxMempool` struct.

All transactions are ordered by priority in `allEntriesSorted`. We'd update this funciton to take nonce into account when ordering.


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

## Status

Proposed

## Consequences

> This section describes the consequences, after applying the decision. All consequences should be summarized here, not just the "positive" ones.

### Positive

### Negative

### Neutral

## References

> Are there any relevant PR comments, issues that led up to this, or articles referenced for why we made the given design choice? If so link them here!

TODO: Reference some related issues here.

- {reference link}
