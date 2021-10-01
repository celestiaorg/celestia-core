# ADR 005: Decouple the PartSetHeader from the BlockID

## Changelog

- 2021-08-01: Initial Draft

## Context

Celestia has multiple commits to the block data via the `DataHash` and the `PartSetHeader` in the `BlockID`. As stated in the [#184](https://github.com/celestiaorg/lazyledger-core/issues/184), we no longer need the `PartSetHeader` for this additional commitment to the block's data. However, we are still planning to use the `PartSetHeader` for block propagation during consensus in the short-medium term. This means that we will remove the `PartSetHeader` from as many places as possible, but keep it in the `Proposal` struct.

## Alternative Approaches

It’s worth noting that there are proposed changes to remove the `PartSetHeader` entirely, and instead use the already existing commitment to block data, the `DataAvailabilityHeader`, to propagate blocks in parallel during consensus. Discussions regarding the detailed differences entailed in each approach are documented in that ADR's PR. The current direction that is described in this ADR is significantly more conservative in its approach, but it is not strictly an alternative to other designs. This is because other designs would also require removal of the `PartSethHeader`, which is a project in and of itself due to the `BlockID` widespread usage throughout tendermint and the bugs that pop up when attempting to remove it. 

## Decision

While we build other better designs to experiment with, we will continue to implement the design specified here as it is not orthogonal. https://github.com/celestiaorg/lazyledger-core/pull/434#issuecomment-869158788

## Detailed Design

- [X] Decouple the BlockID and the PartSetHeader [#441](https://github.com/celestiaorg/lazyledger-core/pull/441)
- [ ] Remove the BlockID from every possible struct other than the `Proposal`
  - [X] Stop signing over the `PartSetHeader` while voting [#457](https://github.com/celestiaorg/lazyledger-core/pull/457)
  - [X] Remove the `PartSetHeader` from the Header [#457](https://github.com/celestiaorg/lazyledger-core/pull/457)
  - [X] Remove the `PartSetHeader` from `VoteSetBits`, `VoteSetMaj23`, and `state.State` [#479](https://github.com/celestiaorg/lazyledger-core/pull/479)
  - [ ] Remove the `PartSetHeader` from other structs


## Status

Proposed

### Positive

- Conservative and easy to implement
- Acts as a stepping stone for other better designs
- Allows us to use 64kb sized chunks, which are well tested

### Negative

- Not an ideal design as we still have to include an extra commitment to the block's data in the proposal

## References

Alternative ADR [#434](https://github.com/celestiaorg/lazyledger-core/pull/434)  
Alternative implementation [#427](https://github.com/celestiaorg/lazyledger-core/pull/427) and [#443](https://github.com/celestiaorg/lazyledger-core/pull/443)  
[Comment](https://github.com/celestiaorg/lazyledger-core/pull/434#issuecomment-869158788) that summarizes decision  