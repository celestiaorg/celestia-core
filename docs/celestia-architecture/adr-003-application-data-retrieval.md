# ADR 003: Retrieving Application messages

## Changelog

- 2021-04-25: initial draft

## Context

This ADR builds on top of [ADR 002](adr-002-ipld-da-sampling.md) and will use the implemented APIs described there.
The reader should familiarize themselves at least with the high-level concepts the as well as in the [specs](https://github.com/celestiaorg/celestia-specs/blob/master/specs/data_structures.md#2d-reed-solomon-encoding-scheme).

The academic [paper](https://arxiv.org/abs/1905.09274) describes the motivation and context for this API.
The main motivation can be quoted from section 3.3 of that paper:

> (Property1) **Application message retrieval partitioning.** Client nodes must be able to download all of the messages relevant to the applications they use [...], without needing to downloading any messages for other applications.

> (Property2) **Application message retrieval completeness.** When client nodes download messages relevant to the applications they use [...], they must be able to verify that the messages they received are the complete set of messages relevant to their applications, for specific
blocks, and that there are no omitted messages.



The main data structure that enables above properties is called a Namespaced Merkle Tree (NMT), an ordered binary Merkle tree where:
1. each node in the tree includes the range of namespaces of the messages in all descendants of each node
2. leaves in the tree are ordered by the namespace identifiers of the leaf messages

A more formal description can be found the [specification](https://github.com/celestiaorg/celestia-specs/blob/de5f4f74f56922e9fa735ef79d9e6e6492a2bad1/specs/data_structures.md#namespace-merkle-tree).
An implementation can be found in [this repository](https://github.com/celestiaorg/nmt).

This ADR basically describes version of the [`GetWithProof`](https://github.com/celestiaorg/nmt/blob/ddcc72040149c115f83b2199eafabf3127ae12ac/nmt.go#L193-L196) of the NMT that leverages the fact that IPFS uses content addressing and that we have implemented an [IPLD plugin](https://github.com/celestiaorg/celestia-core/tree/37502aac69d755c189df37642b87327772f4ac2a/p2p/ipld) for an NMT.

**Note**: The APIs defined here will be particularly relevant for Optimistic Rollup (full) nodes that want to download their Rollup's data (see [celestiaorg/optimint#48](https://github.com/celestiaorg/optimint/issues/48)).
Another potential use-case of this API could be for so-called [light validator nodes](https://github.com/celestiaorg/celestia-specs/blob/master/specs/node_types.md#node-type-definitions) that want to download and replay the state-relevant portion of the block data, i.e. transactions with [reserved namespace IDs](https://github.com/celestiaorg/celestia-specs/blob/master/specs/consensus.md#reserved-namespace-ids).

## Alternative Approaches

The approach described below will rely on IPFS' block exchange protocol (bitswap) and DHT; IPFS's implementation will be used as a black box to find peers that can serve the requested data.
This will likely be much slower than it potentially could be and for a first implementation we intentionally do not incorporate the optimizations that we could.

We briefly mention potential optimizations for the future here:
- Use of [graphsync](https://github.com/ipld/specs/blob/5d3a3485c5fe2863d613cd9d6e18f96e5e568d16/block-layer/graphsync/graphsync.md) instead of [bitswap](https://docs.ipfs.io/concepts/bitswap/) and use of [IPLD selectors](https://github.com/ipld/specs/blob/5d3a3485c5fe2863d613cd9d6e18f96e5e568d16/design/history/exploration-reports/2018.10-selectors-design-goals.md)
- expose an API to be able to download application specific data by namespace (including proofs) with the minimal number of round-trips (e.g. finding nodes that expose an RPC endpoint like [`GetWithProof`](https://github.com/celestiaorg/nmt/blob/ddcc72040149c115f83b2199eafabf3127ae12ac/nmt.go#L193-L196))

## Decision

Most discussions on this particular API happened either on calls or on other non-documented way.
We only describe the decision in this section.

We decide to implement the simplest approach first.
We first describe the protocol informally here and explain why this fulfils (Property1) and (Property2) in the [Context](#context) section above.

In the case that leaves with the requested namespace exist, this basically boils down to the following: traverse the tree starting from the root until finding first leaf (start) with the namespace in question, then directly request and download all leaves coming after the start until the namespace changes to a greater than the requested one again.
In the case that no leaves with the requested namespace exist in the tree, we traverse the tree to find the leaf in the position in the tree where the namespace would have been and download the neighbouring leaves.

This is pretty much what the [`ProveNamespace`](https://github.com/celestiaorg/nmt/blob/ddcc72040149c115f83b2199eafabf3127ae12ac/nmt.go#L132-L146) method does but using IPFS we can simply locate and then request the leaves, and the corresponding inner proof nodes will automatically be downloaded on the way, too.

## Detailed Design

We define one function that returns all shares of a block belonging to a requested namespace and block (via the block's data availability header).
See [`ComputeShares`](https://github.com/celestiaorg/celestia-core/blob/1a08b430a8885654b6e020ac588b1080e999170c/types/block.go#L1371) for reference how encode the block data into namespace shares.

```go
// RetrieveShares returns all raw data (raw shares) of the passed-in
// namespace ID nID and included in the block with the DataAvailabilityHeader dah.
func RetrieveShares(
    ctx context.Context,
    nID namespace.ID,
    dah *types.DataAvailabilityHeader,
    api coreiface.CoreAPI,
) ([][]byte, error) {
    // 1. Find the row root(s) that contains the namespace ID nID
    // 2. Traverse the corresponding tree(s) according to the
    //    above informally described algorithm and get the corresponding
    //    leaves (if any)
    // 3. Return all (raw) shares corresponding to the nID
}

```

Additionally, we define two functions that use the first one above to:
1. return all the parsed (non-padding) data with [reserved namespace IDs](https://github.com/celestiaorg/celestia-specs/blob/de5f4f74f56922e9fa735ef79d9e6e6492a2bad1/specs/consensus.md#reserved-namespace-ids): transactions, intermediate state roots, evidence.
2. return all application specific blobs (shares) belonging to one namespace ID parsed as a slice of Messages ([specification](https://github.com/celestiaorg/celestia-specs/blob/de5f4f74f56922e9fa735ef79d9e6e6492a2bad1/specs/data_structures.md#message) and [code](https://github.com/celestiaorg/celestia-core/blob/1a08b430a8885654b6e020ac588b1080e999170c/types/block.go#L1336)).

The latter two methods might require moving or exporting a few currently unexported functions that (currently) live in [share_merging.go](https://github.com/celestiaorg/celestia-core/blob/1a08b430a8885654b6e020ac588b1080e999170c/types/share_merging.go#L57-L76) and could be implemented in a separate pull request.

```go
// RetrieveStateRelevantMessages returns all state-relevant transactions
// (transactions, intermediate state roots, and evidence) included in a block
// with the DataAvailabilityHeader dah.
func RetrieveStateRelevantMessages(
    ctx context.Context,
    nID namespace.ID,
    dah *types.DataAvailabilityHeader,
    api coreiface.CoreAPI,
) (Txs, IntermediateStateRoots, EvidenceData, error) {
    // like RetrieveShares but for all reserved namespaces
    // additionally the shares are parsed (merged) into the
    // corresponding types in the return arguments
}
```

```go
// RetrieveMessages returns all Messages of the passed-in
// namespace ID and included in the block with the DataAvailabilityHeader dah.
func RetrieveMessages(
    ctx context.Context,
    nID namespace.ID,
    dah *types.DataAvailabilityHeader,
    api coreiface.CoreAPI,
) (Messages, error) {
    // like RetrieveShares but this additionally parsed the shares
    // into the Messages type
}
```

## Status

Proposed

## Consequences

This API will most likely be used by Rollups too.
We should document it properly and move it together with relevant parts from ADR 002 into a separate go-package.

### Positive

- easy to implement with the existing code (see [ADR 002](https://github.com/celestiaorg/celestia-core/blob/47d6c965704e102ae877b2f4e10aeab782d9c648/docs/adr/adr-002-ipld-da-sampling.md#detailed-design))
- resilient data retrieval via a p2p network
- dependence on a mature and well-tested code-base with a large and welcoming community

### Negative

- with IPFS, we inherit the fact that potentially a lot of round-trips are done until the data is fully downloaded; in other words: this could end up way slower than potentially possible
- anyone interacting with that API needs to run an IPFS node

### Neutral

- optimizations can happen incrementally once we have an initial working version

## References

We've linked to all references throughout the ADR.
