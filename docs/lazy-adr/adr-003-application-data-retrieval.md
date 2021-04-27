# ADR 003: Retrieving Application messages

## Changelog

- 2021-04-25: initial draft

## Context

The academic Lazyledger [paper](https://arxiv.org/abs/1905.09274) describes the motivation and context for this API.
The main motivation can be quoted from section 3.3 of that paper:

> **Application message retrieval partitioning.** Client nodes must be able to download all of the messages relevant to the applications they use [...], without needing to downloading any messages for other applications.

> **Application message retrieval completeness.** When client nodes download messages relevant to the applications they use [...]
nodes, they must be able to verify that the messages they received are the complete set of messages relevant to their applications, for specific
blocks, and that there are no omitted messages.



The main data structure that enables above properties is called a Namespaced Merkle Tree (NMT), an ordered binary Merkle tree where:
1. each node in the tree includes the range of namespaces of the messages in all descendants of each node
2. leaves in the tree are ordered by the  namespace identifiers of the leaf messages

A more formal description can be found the [specification](https://github.com/lazyledger/lazyledger-specs/blob/de5f4f74f56922e9fa735ef79d9e6e6492a2bad1/specs/data_structures.md#namespace-merkle-tree).
An implementation can be found in [this repository](https://github.com/lazyledger/nmt).

This ADR basically describes version of the [`GetWithProof`](https://github.com/lazyledger/nmt/blob/ddcc72040149c115f83b2199eafabf3127ae12ac/nmt.go#L193-L196) of the NMT that leverages the fact that IPFS uses content addressing and that we have implemented an [IPLD plugin](https://github.com/lazyledger/lazyledger-core/tree/37502aac69d755c189df37642b87327772f4ac2a/p2p/ipld) for an NMT.

**Note**: The APIs defined here will be particularly relevant for Optimistic Rollup (full) nodes that want to download their Rollup's data (see [lazyledger/optimint#48](https://github.com/lazyledger/optimint/issues/48)).

## Alternative Approaches

The approach described below will rely on IPFS' block exchange protocol (bitswap) and DHT; IPFS's implementation will be used as a black box to find peers that can serve the requested data.
This will likely be much slower than it potentially could be and for a first implementation we intentionally do not incorporate the optimizations that we could.

We briefly mention potential optimizations for the future here:
- Use of [graphsync](https://github.com/ipld/specs/blob/5d3a3485c5fe2863d613cd9d6e18f96e5e568d16/block-layer/graphsync/graphsync.md) instead of [bitswap](https://docs.ipfs.io/concepts/bitswap/) and use of [IPLD selectors](https://github.com/ipld/specs/blob/5d3a3485c5fe2863d613cd9d6e18f96e5e568d16/design/history/exploration-reports/2018.10-selectors-design-goals.md)
- expose an API to be able to download application specific data by namespace (including proofs) with the minimal number of round-trips (e.g. finding nodes that expose an RPC endpoint like [`GetWithProof`](https://github.com/lazyledger/nmt/blob/ddcc72040149c115f83b2199eafabf3127ae12ac/nmt.go#L193-L196))

## Decision

> This section records the decision that was made.
> It is best to record as much info as possible from the discussion that happened. This aids in not having to go back to the Pull Request to get the needed information.

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
> - Does this change require coordination with the LazyLedger fork of the SDK or lazyledger-app?

## Status

Proposed

## Consequences

> This section describes the consequences, after applying the decision. All consequences should be summarized here, not just the "positive" ones.

### Positive

- easy to implement with the existing code (see [ADR 002](https://github.com/lazyledger/lazyledger-core/blob/47d6c965704e102ae877b2f4e10aeab782d9c648/docs/lazy-adr/adr-002-ipld-da-sampling.md#detailed-design))
- resilient data retrieval via a p2p network
- dependence on a mature and well-tested code-base with a large and welcoming community
-

### Negative

- with IPFS, we inherit the fact that potentially a lot of round-trips are done until the data is fully downloaded
- in other words: this could end up way slower than potentially possible

### Neutral

- optimizations can happen incrementally once we have an initial working version

## References

> Are there any relevant PR comments, issues that led up to this, or articles referenced for why we made the given design choice? If so link them here!

- IPLD selectors: https://github.com/ipld/specs/blob/5d3a3485c5fe2863d613cd9d6e18f96e5e568d16/selectors/selectors.md
