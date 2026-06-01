- Log a warning before `PruneBlocks` works through a large block backlog (more
  than 100 blocks), reporting how many blocks will be pruned and that it pauses
  block syncing until complete, so the synchronous pause is expected instead of
  looking like a hang.
  ([\#3073](https://github.com/celestiaorg/celestia-core/pull/3073))
