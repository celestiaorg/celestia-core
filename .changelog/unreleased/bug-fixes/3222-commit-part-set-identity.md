- `[consensus]` Require the committed part set, not just a matching
  `BlockID.Hash`, before finalizing a commit. `enterCommit` now refetches when
  the part set header differs, `addVote` clears `ProposalBlock` when it discards
  the parts that block was decoded from, and `tryFinalizeCommit` waits for a
  complete matching part set instead of letting `finalizeCommit` and the block
  store panic.
  ([\#3222](https://github.com/celestiaorg/celestia-core/issues/3222))
