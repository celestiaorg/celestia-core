- Log progress while `PruneBlocks` works through a large block backlog (warning
  above 100 blocks, progress every 1000, completion log), so the synchronous
  block-sync pause is observable instead of looking like a hang.
  ([\#3073](https://github.com/celestiaorg/celestia-core/pull/3073))
