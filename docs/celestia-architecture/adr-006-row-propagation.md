# ADR 006: Consensus Block Gossiping with Rows

## Changelog
* 24.06.2021 - Initial description
* 07.07.2021 - More important details were added
* 18.08.2021 - Mention alternative approaches briefly

## Context
It's a long story of relations between Celestia, Tendermint, and consensus block gossiping. Celestia's team discussed
multiple ideas, several ADRs were made, and nothing yet was finalized. This ADR is another attempt to bring valuable
changes into block gossiping and hopefully successful.

Currently, we inherit the following from Tendermint. Our codebase relies on the blocks Parts notion. Each Part is a
piece of an entire serialized block. Those Parts are gossiped between nodes in consensus and committed with
`PartSetHeader` containing a Merkle Root of the Parts. However, Parts gossiping wasn't designed for Celestia blocks.

Celestia comes with a different block representation from Tendermint. It lays out Blocks as a table of data shares,
where Rows or Columns can be and should be gossiped instead of Parts, keeping only one system-wide commitment to data.

## Alternative Approaches
### ["nah it works just don't touch it"](https://ahseeit.com//king-include/uploads/2020/11/121269295_375504380484919_2997236194077828589_n-6586327691.jpg) approach

It turns out that we could fully treat the Tendermint consensus as a black box, keeping two data commitments: one for
consensus with `PartSetHeader` and another for the world outside the consensus with `DAHeader`.

#### Pros
* Less work

### Others
* get rid of the PartsHeader from BlockID without changing block propagation at all (see [ADR 005](https://github.com/celestiaorg/celestia-core/blob/58a3901827afbf97852d807de34a2b66f93e0eb6/docs/lazy-adr/adr-005-decouple-blockid-and-partsetheader.md#adr-005-decouple-the-partsetheader-from-the-blockid))
* change block propagation to fixed-sized chunks but based on the ODS instead of how Parts are built currently (for this we have empirical evidence of how it performs in practice)
* send the block as a whole (only works with smaller blocks)
* block propagation-based on sending the header and Tx-IDs and then requesting the Tx/Messages that are missing from the local mempool of a node on demand

#### Cons
* Pulls two data commitments to Celestia's specs
* Brings ambiguity to data integrity verification
* Controversial from software design perspective
* Brings DOSing vector for big Blocks. Every Block would need to be represented in two formats in RAM
* Wastes more resources on building and verifying additional

## Decision
The decision is to still treat Tendermint's consensus as a black box, but with few amendments to gossiping mechanism:
* Introduce `RowSet` that mimics `PartSet`.

  `RowSet` is a helper structure that wraps DAHeader and tracks received Rows with their integrity against DAHeader and
  tells its user when the block is complete and/or can be recovered. Mostly it is a helper and is not a high-level
  concept.
* Replace `PartSet` with `RowSet` within consensus.
* Keep `DAHeader` in `Proposal`
* Remove `PartSetHeader` from `Proposal`

The changes above are required to implement the decision. At later point, other changes listed below are
likely to be implemented as a clean-up:
* Entirely removing `PartSetHeader`, as redundant data commitment
* Removing `PartSet`
* Relying on `DAHeader` instead of `PartSetHeader`

## Detailed Design
The detailed design section demonstrates the design and supporting changes package by package. Fortunately, the
design does not affect any public API and changes are solely internal.

### `types`
#### RowSet and Row
First and essential part is to implement `RowSet` and `Row`, fully mimicking semantics of `PartSet` and `Part` to
decrease the number of required changes. Below, implementation semantics are presented:

```go
// Row represents a blob of multiple ExtendedDataSquare shares.
// Practically, it is half of an extended row, as other half can be recomputed.
type Row struct {
// Index is an top-to-bottom index of a Row in ExtendedDataSquare.
// NOTE: Row Index is unnecessary, as we can determine it's Index by hash from DAHeader. However, Index removal
// would bring more changes to Consensus Reactor with arguable pros of less bandwidth usage.
Index int
// The actual share blob.
Data []byte
}

// NewRow creates new Row from flattened shares and index.
func NewRow(idx int, row [][]byte) *Row

// RowSet wraps DAHeader and tracks added Rows with their integrity against DAHeader.
// It allows user to check whenever rsmt2d.ExtendedDataSquare can be recovered.
//
// RowSet tracks the whole ExtendedDataSquare, Where Q0 is the original block data:
//  ----  ----
// | Q0 || Q1 |
//  ----  ----
// | Q2 || Q3 |
//  ----  ----
//
// But its AddRow and GetRow methods accepts and returns only half of the Rows - Q0 and Q2. Q1 and Q3 are recomputed.
//  ----
// | Q0 |
//  ----
// | Q2 |
//  ----
//
type RowSet interface {
// NOTE: The RowSet is defined as an interface for simplicity. In practice it should be a struct with one and only
// implementation.

// AddRow adds a Row to the set. It returns true with nil error in case Row was successfully added.
// The logic for Row is:
//  * Check if it was already added
//  * Verify its size corresponds to DAHeader
//  * Extend it with erasure coding and compute a NMT Root over it
//  * Verify that the NMT Root corresponds to DAHeader Root under its Index
//  * Finally add it to set and mark as added.
//
AddRow(*Row) (bool, error)

// GetRow return of a Row by its index, if exist.
GetRow(i int) *Row

// Square checks if enough rows were added and returns recomputed ExtendedDataSquare if enough
Square() (*rsmt2d.ExtendedDataSquare, error)

// other helper methods are omitted
}

// NewRowSet creates full RowSet from rsmt2d.ExtendedDataSquare to gossip it to others through GetRow.
func NewRowSet(eds *rsmt2d.ExtendedDataSquare) *RowSet

// NewRowSetFromHeader creates empty RowSet from a DAHeader to receive and verify gossiped Rows against the DAHeader
// with AddRow.
func NewRowSetFromHeader(dah *ipld.DataAvailabilityHeader) *RowSet
```

#### Vote
`Vote` should include a commitment to data. Previously, it relied on `PartSetHeader` in `BlockId`, instead it relies on
added `DAHeader`. Protobuf schema is updated accordingly.

#### Proposal
`Proposal` is extended with `NumOriginalDataShares`. This is an optimization that
helps Validators to populate Header without counting original data shares themselves from a block received form a
Proposer. Potentially, that introduce a vulnerability by which a Proposer can send wrong value, leaving the populated
Header of Validators wrong. This part of the decision is optional.

### `consenSUS`
#### Reactor
##### Messages
The decision affects two messages on consensus reactor:
* `BlockPartMessage` -> `BlockRowMessage`
  * Instead of `Part` it carries `Row` defined above.
* `NewValidBlockMessage`
  * Instead of `PartSetHeader` it carries `DAHeader`
  * `BitArray` of `RowSet` instead of `PartSet`
    Protobuf schema for both is updated accordingly.

##### PeerRoundState
`PeerRoundState` tracks state of each known peer in a round, specifically what commitment it has for a Block and what
chunks peer holds. The decision changes it to track `DAHeader` instead of `PartSetHeader`, along with `BitArray` of
`RowSet` instead of `PartSet`.

##### BlockCatchup
The Reactor helps its peers to catchup if they go out of sync. Instead of sending random `Part` it now sends random
`Row` by `BlockRowMessage`. Unfortunately, that requires the Reactor to load whole Block from store. As an optimization,
an ability to load Row only from the store could be introduced at later point.

#### State
##### RoundState
The RoundState keeps Proposal, Valid and Lock Block's data. Along with an entire Block and its Parts, the RoundState
also keeps Rows using `RowSet`. At later point, `PartSet` that tracks part can be removed.

##### Proposal Stage
Previously, the State in proposal stage waited for all Parts to assemble the entire Block. Instead, the State waits for
the half of all Rows from a proposer and/or peers to recompute the Block's data and notifies them back that no more
needs to be sent. Also, through Rows, only minimally required amount of information is gossiped. Everything else to
assemble the full Block is collected from own chain State and Proposal.

## Status
Proposed

## Consequences
### Positive
* Hardening of consensus gossiping with erasure coding
* Blocks exceeding the size limit are immediately rejected on Proposal, without the need to download an entire Block.
* More control over Row message size during consensus, comparing to Part message, as last part of the block always has
  unpredictable size. `DAHeader`, on the other hand, allows knowing precisely the size of Row messages.
* Less bandwidth usage
  * Only required Block's data is gossiped.
  * Merkle proofs of Parts are not sent on the wire
* Only one system-wide block data commitment schema
* We don't abandon the work we were doing for months and taking profits out of it
  * PR [#287](https://github.com/celestiaorg/lazyledger-core/pull/287)
  * PR [#312](https://github.com/celestiaorg/lazyledger-core/pull/312)
  * PR [#427](https://github.com/celestiaorg/lazyledger-core/pull/427)
  * and merged others

### Negative
* We invest some more time(~1.5 weeks).
  * Most of the work is done. Only few changes left in the implementation along with peer reviews.

### Neutral
* Rows vs Parts on the wire
  * Previously, parts were propagated with max size of 64KiB. Let's now take a Row of the largest 128x128 block in
    comparison. The actual data size in such a case for the Row would be 128x256(shares_per_row*share_size)=32KiB, which
    is exactly two times smaller than a Part.
* Gossiped chunks are no longer constant size. Instead, their size is proportional to the size of Block's data.
* Another step back from original Tendermint's codebases