- `[blocksync]` Cross-validate ExtendedCommit against LastCommit before
  persisting during block sync to prevent a malicious peer from injecting a
  corrupted ExtendedCommit that causes a panic on consensus restart.
