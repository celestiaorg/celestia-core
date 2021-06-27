# ADR 006: Block Propagation with Rows

## Changelog
* 24.06.2021

## Context
It's a long story of relations between Celestia, Tendermint, and consensus block gossiping. Celestia's team discussed multiple ideas, several ADRs were made, and nothing yet was finalized. The ADR is another attempt to bring changes into block gossiping and hopefully successful.

Currently, we inherit the following from Tendermint. Our codebase relies on the blocks Parts notion, which is a piece of an entire serialized block. Those Parts are gossiped between nodes in consensus and committed with PartSetHeader containing a Merkle Root of the Parts. However, Parts gossiping wasn't designed for Celestia blocks.

Celestia comes with a different block representation from Tendermint. It lays out block as a table of data shares, where Rows or Columns can be and should be gossiped instead of Parts, keeping only one system-wide commitment to data.

## Alternative Approaches
### ["nah it works just don't touch it"](https://ahseeit.com//king-include/uploads/2020/11/121269295_375504380484919_2997236194077828589_n-6586327691.jpg) approach

It turns out that we could fully treat the Tendermint consensus as a black box. However, that means that we need to verify block integrity twice for both PartSetHeader and DAHeader.

#### Pros
* Less work

#### Cons
* This isn't adequate in terms of protocol and software design
* Potentially introduces security issues(implementer can forget/miss secondary data verification)
* Wastes more CPU cycles on building and verifying additional Merkle Tree
* Extends DOSing vector for huge blocks cause to verify a block, you need to verify its two complete RAM copies in different layouts.
* We would also need to add PartSetHeader to Celestia-specs and bring there such an ugliness

## Decision
The decision is to treat Tendermint's consensus still and its gossipping as a black box, but with few amendments:
* Entirely removing PartSetHeader, as redundant data commitment
* Placing DAHeader instead of PartSetHeader
* Implementing RowSet and mimicking it to PartSet
* Removing PartSet

## Detailed Design
Fortunately, the above decision does not affect any public API, and changes are solely internal, mostly in `consensus` package, but others are also slightly affected.

The essential part is to implement RowSet. Mimicking it to RowSet decreases the number of changes, as we can swap it with PartSet. However, the initial implementation will send fully erasured data rows to peers, leaving a gap for improvement by sending actual data only later. The current state of the rsmt2d library does not have any API to work with rows, only with the entire data square. Without row/col level erasure-coding API, RowSet would not check the integrity of a non-erasured Row against DAHeader, but that wouldn't be problematic to improve later.

## Status
Proposed

## Consequences
### Positive
* We don't have all the cons of "nah it works just don't touch it" approach
* We don't abandon the work we were doing for months and taking profits out of it
    * PR [#287](https://github.com/celestiaorg/lazyledger-core/pull/287)
    * PR [#312](https://github.com/celestiaorg/lazyledger-core/pull/312)
    * PR [#427](https://github.com/celestiaorg/lazyledger-core/pull/427)
    * and merged others

### Negative
* We invest some more time(~1.5 weeks). Although most of the work is done and we need to do the latest spurt to rewrite tests and merge all of the work.

### Neutral
* Initially, we send redundant block data, but later we can fix that after rsmt2d API changes.
