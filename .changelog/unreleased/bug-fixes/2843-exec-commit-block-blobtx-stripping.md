- Strip BlobTx wrappers in `ExecCommitBlock` to match `applyBlock` behavior,
  fixing a crash recovery replay determinism issue where different transaction
  bytes were sent to `FinalizeBlock` during replay vs normal execution.
