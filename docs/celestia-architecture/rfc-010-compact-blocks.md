# RFC 010: Compact Blocks

## Changelog

- 02.02.2022: Initial draft (@cmwaters)

## Context

This is a request for comment on an initial working prototype of a "compact blocks" implementation. The aim is to outline the current design and solicit early feedback. Whereas performance based testing may provide a quantitative analysis and help with fine tuning certain parameters, the RFC should compliment this by focusing on general code patterns, security threats, compatibility/upgradeability as well as other engineering requirements. The conclusion of both parts should result in an ADR documenting both the decision and implementation.

### Background

"Compact blocks" is a shorthand way of referring to a protocol whereby the data in a block is represented as a series of hashes and instead of broadcasting the entire block only these hashes are broadcasted. The nodes in the network reconstruct the proposed block from the data they already have or by requesting the missing parts to peers that have signalled having that data. The critical assumption the protocol makes is that most nodes in a network already have most of the data that the proposer is proposing. The only information that is actually new is the order and inclusion of those transactions. This has echoes of the Narwhal paper which highlighted the separation between data dissemination and consensus.

More practically in regards to Tendermint, compact blocks eliminates the need for consensus to resend all the transactions that the mempool already has distributed.

### Philosophy

At a high level there have been two approaches to the problem of data dissemination in a single leader network:

The first is that all nodes send their transactions to the leader and the leader batches them together and then sends the total set to all other nodes. This pattern is what was outlined in Raft. This requires that the location and identity of the proposer is known in advance. It is efficient in that in the happy case, there is no duplication of transmission.

## Alternative Approaches

> This section contains information around alternative options that are considered before making a decision. It should contain a explanation on why the alternative approach(es) were not chosen.

## Design

The design can be divided into two parts that map to the two components: consensus and mempool. The design tries to maintain the existing separation of concerns with the mempool responsible for transaction dissemination, and consensus, for agreement on the order and inclusion of a set of transactions. The boundary between the two is defined by the new interface:

```go
type TxFetcher interface {
	// For constructing the compact block
	FetchKeysFromTxs(ctx context.Context, blockID []byte, txs [][]byte) ([][]byte, error)
	// For reconstructing the full block from the compact block
	FetchTxsFromKeys(ctx context.Context, blockID []byte, compactData [][]byte) ([][]byte, error)
}
```

As the mempool is depended by consesnsus, we begin there. It is helpful, first, to get a familiarity of the existing mechanism behind the content addressable transaction pool. This can be done by reading the spec and ADR. Only the v2 mempool (CAT pool) supports compact blocks and must be enabled.

### Mempool

The mempool implements the `TxFetcher` interface. The first of these methods is the more trivial to implement. When `FetchKeysFromTxs` is called, the mempool loops through all transactions, generating the key as the sha256 hash. As new transactions can be added in ABCI's `PrepareProposal`, it must filter out these new transactions and immediately broadcast them to all connected peers. It returns the complete list of transaction hashes.

`FetchTxsFromKeys`, does the opposite, for every key it recognises it fetches the respective transaction. For those that it currently doesn't have it checks to see if it has seen a `SeenTx` from any of it's peers. If so, it requests the transactions from them so long as it already doesn't have an outbound request. Following this, it waits for responses to come back with the transactions, rerequesting until the full block can be reconstructed. If it doesn't have the tx or doesn't know of anyone with the transaction, it waits with the expectation that the proposer or one of its connected peers will send the missing transaction. If at anypoint the context times out, it aborts and returns an empty slice with the context error.


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
